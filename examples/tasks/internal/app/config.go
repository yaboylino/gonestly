package app

import (
	"fmt"
	"log/slog"

	"github.com/yaboylino/gonestly"
)

// Config is read from environment variables and the .env file.
type Config struct {
	Port        int    `default:"3000"`
	DatabaseURL string `env:"DATABASE_URL,required"`
	// CORSOrigins are the origins of the frontends that may call the API from a browser.
	CORSOrigins []string `env:"CORS_ORIGINS"`
	// Docs serves the OpenAPI document and Swagger UI; set DOCS=false to hide them.
	Docs bool `default:"true"`
}

// NewServerConfig listens on the configured port, gives every request an ID and a log line,
// and answers the browsers of the configured origins.
func NewServerConfig(cfg Config, logger *slog.Logger) gonestly.ServerConfig {
	return gonestly.ServerConfig{
		Addr:       fmt.Sprintf(":%d", cfg.Port),
		Middleware: []gonestly.Middleware{gonestly.RequestID(), gonestly.AccessLog(logger)},
		CORS:       gonestly.CORSConfig{AllowedOrigins: cfg.CORSOrigins},
	}
}

// NewOpenAPIConfig describes the API in its documentation.
func NewOpenAPIConfig(cfg Config) gonestly.OpenAPIConfig {
	return gonestly.OpenAPIConfig{Title: "Tasks", Version: "0.1.0", Disabled: !cfg.Docs}
}
