module github.com/yaboylino/gonestly/validator

go 1.26.0

toolchain go1.26.11

require (
	github.com/go-playground/validator/v10 v10.30.6
	github.com/yaboylino/gonestly v0.1.0
)

require (
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

// In this repository the module is developed against the framework next to it; a project that
// requires this module uses the released framework.
replace github.com/yaboylino/gonestly => ../
