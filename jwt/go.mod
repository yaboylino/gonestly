module github.com/yaboylino/gonestly/jwt

go 1.26.0

toolchain go1.26.11

require (
	github.com/MicahParks/keyfunc/v3 v3.9.2
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/yaboylino/gonestly v0.2.0
)

require (
	github.com/MicahParks/jwkset v0.11.4 // indirect
	golang.org/x/time v0.15.0 // indirect
)

// In this repository the module is developed against the framework next to it; a project that
// requires this module uses the released framework.
replace github.com/yaboylino/gonestly => ../
