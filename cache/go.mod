module github.com/yaboylino/gonestly/cache

go 1.26.0

toolchain go1.26.11

require github.com/yaboylino/gonestly v0.3.0

// In this repository the module is developed against the framework next to it; a project that
// requires this module uses the released framework.
replace github.com/yaboylino/gonestly => ../
