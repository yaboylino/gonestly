package gonestly

// Module groups related providers and declares which of them other modules may use.
//
// Declare a module once as a package-level pointer and import that pointer wherever it is needed:
// a module is constructed once per application, however many modules import it.
type Module struct {
	// Name identifies the module in error messages and logs. It must be unique within an application.
	Name string
	// Imports are the modules whose exported providers this module can use.
	Imports []*Module
	// Providers are constructed by the application and can be injected into each other.
	Providers []Provider
	// Controllers handle HTTP requests. They are constructed like providers and can inject
	// everything this module can, but they cannot be injected or exported themselves.
	Controllers []Provider
	// Exports are the providers that modules importing this module can use.
	Exports []Export
	// Global makes the exported providers available to every module without importing it.
	Global bool
}

// Controllers turns controller constructors into providers, like Providers.
func Controllers(items ...any) []Provider {
	return Providers(items...)
}

// Imports returns its arguments as a slice, for symmetry with Providers and Exports.
func Imports(modules ...*Module) []*Module {
	return modules
}
