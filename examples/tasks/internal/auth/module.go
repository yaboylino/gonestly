// Package auth protects routes with an API token.
package auth

import (
	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/config"
)

// Config holds the token that clients send to change data.
type Config struct {
	APIToken string `env:"API_TOKEN,required"`
}

// Module provides the guard to the modules that import it.
var Module = &gonestly.Module{
	Name:      "auth",
	Imports:   gonestly.Imports(config.Module[Config](config.Options{})),
	Providers: gonestly.Providers(NewGuard),
	Exports:   gonestly.Exports(NewGuard),
}
