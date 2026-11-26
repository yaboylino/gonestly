// Package health tells whether the application is running and can handle requests.
package health

import "github.com/yaboylino/gonestly"

// Module serves the health checks.
var Module = &gonestly.Module{
	Name:        "health",
	Controllers: gonestly.Controllers(NewController),
}
