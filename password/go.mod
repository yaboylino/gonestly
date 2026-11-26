module github.com/yaboylino/gonestly/password

go 1.26.0

toolchain go1.26.11

require github.com/yaboylino/gonestly v0.4.0

require (
	golang.org/x/crypto v0.59.0
	golang.org/x/sys v0.50.0 // indirect
)

// In this repository the module is developed against the framework next to it; a project that
// requires this module uses the released framework.
replace github.com/yaboylino/gonestly => ../
