// Package gonestly is a modular application framework for Go.
//
// An application is a tree of modules. Each module declares the providers it
// constructs, the controllers it serves and the providers it exports to the
// modules that import it. The framework resolves the dependency graph when the
// application starts, so a missing or unexported dependency is reported before
// the first request is served.
package gonestly
