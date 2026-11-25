package gonestly

import (
	"errors"
	"strings"
)

var (
	// ErrInvalidModule reports a module, provider or export that is declared incorrectly.
	ErrInvalidModule = errors.New("invalid module")
	// ErrMissingDependency reports a dependency that is not available where it is needed.
	ErrMissingDependency = errors.New("missing dependency")
	// ErrDependencyCycle reports providers or modules that depend on each other.
	ErrDependencyCycle = errors.New("dependency cycle")
	// ErrInvalidRoute reports a route that is declared incorrectly or conflicts with another.
	ErrInvalidRoute = errors.New("invalid route")
	// ErrNotStarted reports use of an application that is not running.
	ErrNotStarted = errors.New("application not started")
)

// BuildError lists every problem New found in the module graph.
type BuildError struct {
	Problems []error
}

func (e *BuildError) Error() string {
	var b strings.Builder
	b.WriteString("gonestly: the application cannot be built:")
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.Error())
	}
	return b.String()
}

// Unwrap returns the problems, so errors.Is and errors.As can inspect them.
func (e *BuildError) Unwrap() []error {
	return e.Problems
}
