package gonestly

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// container holds the resolved module graph and the order in which providers are constructed.
type container struct {
	root    *moduleNode
	modules []*moduleNode
	order   []*providerNode
}

type moduleNode struct {
	module      *Module
	imports     []*moduleNode
	providers   []*providerNode
	exports     map[reflect.Type]*providerNode
	exportOrder []reflect.Type
	visible     map[reflect.Type]*providerNode
}

type providerNode struct {
	provider Provider
	module   *moduleNode
	// declaredPtr is the constructor the module declared, which exports refer to even when overridden.
	declaredPtr uintptr
	// deps holds one entry per constructor parameter; nil marks the injected context.
	deps     []*providerNode
	instance reflect.Value
}

func (n *providerNode) String() string {
	return fmt.Sprintf("%s in module %q", n.provider.name, n.module.module.Name)
}

func (n *providerNode) build(ctx context.Context) error {
	args := make([]reflect.Value, len(n.deps))
	for i, dep := range n.deps {
		if dep == nil {
			args[i] = reflect.ValueOf(&ctx).Elem()
			continue
		}
		args[i] = dep.instance
	}
	v, err := n.provider.call(args)
	if err != nil {
		return err
	}
	n.instance = v
	return nil
}

type override struct {
	provider Provider
	used     bool
}

type builder struct {
	nodes     map[*Module]*moduleNode
	names     map[string]*Module
	visiting  map[*Module]bool
	stack     []*Module
	modules   []*moduleNode
	overrides map[reflect.Type]*override
	problems  []error
}

func (b *builder) problem(format string, args ...any) {
	b.problems = append(b.problems, fmt.Errorf(format, args...))
}

func (b *builder) failed() error {
	if len(b.problems) == 0 {
		return nil
	}
	return &BuildError{Problems: b.problems}
}

// buildContainer validates the module graph below root and resolves every dependency.
func buildContainer(root, core *Module, overrides []Provider) (*container, error) {
	b := &builder{
		nodes:     map[*Module]*moduleNode{},
		names:     map[string]*Module{},
		visiting:  map[*Module]bool{},
		overrides: map[reflect.Type]*override{},
	}
	b.registerOverrides(overrides)
	if root == nil {
		b.problem("%w: the root module is nil", ErrInvalidModule)
		return nil, b.failed()
	}
	b.visitModule(core, nil)
	rootNode := b.visitModule(root, nil)
	var unused []string
	for _, o := range b.overrides {
		if !o.used {
			unused = append(unused, fmt.Sprintf("override %s replaces no provider of %s", o.provider.name, o.provider.typ))
		}
	}
	slices.Sort(unused)
	for _, u := range unused {
		b.problem("%w: %s", ErrInvalidModule, u)
	}
	if err := b.failed(); err != nil {
		return nil, err
	}

	b.resolveExports()
	if err := b.failed(); err != nil {
		return nil, err
	}

	b.resolveVisibility()
	b.resolveDependencies()
	if err := b.failed(); err != nil {
		return nil, err
	}

	order := b.sortProviders()
	if err := b.failed(); err != nil {
		return nil, err
	}
	return &container{root: rootNode, modules: b.modules, order: order}, nil
}

func (b *builder) registerOverrides(overrides []Provider) {
	for _, p := range overrides {
		switch {
		case p.err != nil:
			b.problem("%w: override: %w", ErrInvalidModule, p.err)
		case p.typ == nil:
			b.problem("%w: empty override; create it with Provide, As or Value", ErrInvalidModule)
		case b.overrides[p.typ] != nil:
			b.problem("%w: %s is overridden twice", ErrInvalidModule, p.typ)
		default:
			b.overrides[p.typ] = &override{provider: p}
		}
	}
}

// visitModule registers a module and its imports, depth first, so imports precede importers.
func (b *builder) visitModule(m *Module, parent *Module) *moduleNode {
	if m == nil {
		b.problem("%w: module %q imports a nil module", ErrInvalidModule, parent.Name)
		return nil
	}
	if n, ok := b.nodes[m]; ok {
		return n
	}
	if b.visiting[m] {
		start := slices.Index(b.stack, m)
		names := make([]string, 0, len(b.stack)-start+1)
		for _, s := range b.stack[start:] {
			names = append(names, fmt.Sprintf("%q", s.Name))
		}
		names = append(names, fmt.Sprintf("%q", m.Name))
		b.problem("%w: modules import each other: %s", ErrDependencyCycle, strings.Join(names, " -> "))
		return nil
	}

	switch {
	case m.Name == "" && parent == nil:
		b.problem("%w: the root module has no name", ErrInvalidModule)
	case m.Name == "":
		b.problem("%w: a module imported by %q has no name", ErrInvalidModule, parent.Name)
	case b.names[m.Name] != nil:
		b.problem("%w: two different modules are named %q", ErrInvalidModule, m.Name)
	default:
		b.names[m.Name] = m
	}

	b.visiting[m] = true
	b.stack = append(b.stack, m)
	node := &moduleNode{module: m}
	for _, imp := range m.Imports {
		if child := b.visitModule(imp, m); child != nil {
			node.imports = append(node.imports, child)
		}
	}
	b.stack = b.stack[:len(b.stack)-1]
	delete(b.visiting, m)

	seen := map[reflect.Type]*providerNode{}
	for _, p := range m.Providers {
		if p.err != nil {
			b.problem("%w: module %q: %w", ErrInvalidModule, m.Name, p.err)
			continue
		}
		if p.typ == nil {
			b.problem("%w: module %q has an empty provider; create it with Provide, As or Value", ErrInvalidModule, m.Name)
			continue
		}
		declaredPtr := p.fnPtr
		if o := b.overrides[p.typ]; o != nil {
			o.used = true
			p = o.provider
		}
		if prev := seen[p.typ]; prev != nil {
			b.problem("%w: module %q provides %s twice, by %s and %s", ErrInvalidModule, m.Name, p.typ, prev.provider.name, p.name)
			continue
		}
		pn := &providerNode{provider: p, module: node, declaredPtr: declaredPtr}
		seen[p.typ] = pn
		node.providers = append(node.providers, pn)
	}

	b.nodes[m] = node
	b.modules = append(b.modules, node)
	return node
}

func (b *builder) resolveExports() {
	for _, n := range b.modules {
		n.exports = map[reflect.Type]*providerNode{}
		for _, e := range n.module.Exports {
			switch {
			case e.err != nil:
				b.problem("%w: module %q: %w", ErrInvalidModule, n.module.Name, e.err)
			case e.module != nil:
				imp := n.importOf(e.module)
				if imp == nil {
					b.problem("%w: module %q exports module %q, which it does not import", ErrInvalidModule, n.module.Name, e.module.Name)
					continue
				}
				for _, typ := range imp.exportOrder {
					b.addExport(n, imp.exports[typ])
				}
			case e.fnPtr != 0 || e.typ != nil:
				matches := n.ownProviders(e)
				switch len(matches) {
				case 0:
					b.problem("%w: module %q exports %s, which is not one of its providers", ErrInvalidModule, n.module.Name, e.label)
				case 1:
					b.addExport(n, matches[0])
				default:
					b.problem("%w: module %q exports %s, which matches %d providers; export it with gonestly.Type instead", ErrInvalidModule, n.module.Name, e.label, len(matches))
				}
			default:
				b.problem("%w: module %q has an empty export; create it with Exports or Type", ErrInvalidModule, n.module.Name)
			}
		}
	}
}

func (b *builder) addExport(n *moduleNode, pn *providerNode) {
	typ := pn.provider.typ
	if prev := n.exports[typ]; prev != nil {
		if prev != pn {
			b.problem("%w: module %q exports two providers of %s: %s and %s", ErrInvalidModule, n.module.Name, typ, prev, pn)
		}
		return
	}
	n.exports[typ] = pn
	n.exportOrder = append(n.exportOrder, typ)
}

func (n *moduleNode) importOf(m *Module) *moduleNode {
	for _, imp := range n.imports {
		if imp.module == m {
			return imp
		}
	}
	return nil
}

// ownProviders finds the providers an export refers to; one constructor can back several, e.g. via As.
func (n *moduleNode) ownProviders(e Export) []*providerNode {
	var matches []*providerNode
	for _, pn := range n.providers {
		if (e.fnPtr != 0 && pn.declaredPtr == e.fnPtr) || (e.typ != nil && pn.provider.typ == e.typ) {
			matches = append(matches, pn)
		}
	}
	return matches
}

// resolveVisibility computes what each module can inject: global exports, then the exports of
// its imports, then its own providers, each taking precedence over the previous.
func (b *builder) resolveVisibility() {
	globals := map[reflect.Type]*providerNode{}
	for _, n := range b.modules {
		if !n.module.Global {
			continue
		}
		for _, typ := range n.exportOrder {
			pn := n.exports[typ]
			if prev := globals[typ]; prev != nil && prev != pn {
				b.problem("%w: two global modules export %s: %s and %s", ErrInvalidModule, typ, prev, pn)
				continue
			}
			globals[typ] = pn
		}
	}

	for _, n := range b.modules {
		visible := maps.Clone(globals)
		imported := map[reflect.Type]*providerNode{}
		for _, imp := range n.imports {
			for _, typ := range imp.exportOrder {
				pn := imp.exports[typ]
				if prev := imported[typ]; prev != nil && prev != pn {
					b.problem("%w: module %q imports two providers of %s: %s and %s", ErrInvalidModule, n.module.Name, typ, prev, pn)
					continue
				}
				imported[typ] = pn
				visible[typ] = pn
			}
		}
		for _, pn := range n.providers {
			visible[pn.provider.typ] = pn
		}
		n.visible = visible
	}
}

func (b *builder) resolveDependencies() {
	for _, n := range b.modules {
		for _, pn := range n.providers {
			for _, param := range pn.provider.params {
				if param == contextType {
					pn.deps = append(pn.deps, nil)
					continue
				}
				dep, ok := n.visible[param]
				if !ok {
					b.problems = append(b.problems, b.missingDependency(pn, param))
					continue
				}
				pn.deps = append(pn.deps, dep)
			}
		}
	}
}

// missingDependency explains why a dependency is unavailable and how to make it available.
func (b *builder) missingDependency(pn *providerNode, param reflect.Type) error {
	n := pn.module
	var hints []string
	for _, other := range b.modules {
		for _, candidate := range other.providers {
			if candidate.provider.typ != param {
				continue
			}
			switch {
			case other.exports[param] == candidate:
				hints = append(hints, fmt.Sprintf("module %q exports it: add that module to the Imports of %q", other.module.Name, n.module.Name))
			case n.importOf(other.module) != nil:
				hints = append(hints, fmt.Sprintf("module %q provides it but does not export it: add it to the Exports of %q", other.module.Name, other.module.Name))
			default:
				hints = append(hints, fmt.Sprintf("module %q provides it but does not export it: add it to the Exports of %q and import that module", other.module.Name, other.module.Name))
			}
		}
	}

	if len(hints) == 0 {
		if param.Kind() == reflect.Pointer {
			if _, ok := n.visible[param.Elem()]; ok {
				hints = append(hints, fmt.Sprintf("%s is available: did you mean that type?", param.Elem()))
			}
		} else if _, ok := n.visible[reflect.PointerTo(param)]; ok {
			hints = append(hints, fmt.Sprintf("%s is available: did you mean that type?", reflect.PointerTo(param)))
		}
		if param.Kind() == reflect.Interface {
			var implementers []string
			for typ, candidate := range n.visible {
				if typ.Implements(param) {
					implementers = append(implementers, fmt.Sprintf("%s implements it: provide it with gonestly.As[%s](%s)", typ, param, candidate.provider.name))
				}
			}
			slices.Sort(implementers)
			hints = append(hints, implementers...)
		}
	}
	if len(hints) == 0 {
		hints = append(hints, fmt.Sprintf("no module provides it: add a provider for it to %q or to a module it imports", n.module.Name))
	}
	return fmt.Errorf("%w: %s needs %s, which is not available there; %s", ErrMissingDependency, pn, param, strings.Join(hints, "; "))
}

// sortProviders orders providers so that every provider follows its dependencies.
func (b *builder) sortProviders() []*providerNode {
	const (
		unvisited = iota
		visiting
		done
	)
	state := map[*providerNode]int{}
	var path, order []*providerNode

	var visit func(pn *providerNode)
	visit = func(pn *providerNode) {
		switch state[pn] {
		case done:
			return
		case visiting:
			start := slices.Index(path, pn)
			names := make([]string, 0, len(path)-start+1)
			for _, p := range path[start:] {
				names = append(names, p.provider.name)
			}
			names = append(names, pn.provider.name)
			b.problem("%w: %s", ErrDependencyCycle, strings.Join(names, " -> "))
			return
		}
		state[pn] = visiting
		path = append(path, pn)
		for _, dep := range pn.deps {
			if dep != nil {
				visit(dep)
			}
		}
		path = path[:len(path)-1]
		state[pn] = done
		order = append(order, pn)
	}

	for _, n := range b.modules {
		for _, pn := range n.providers {
			if state[pn] == unvisited {
				visit(pn)
			}
		}
	}
	return order
}
