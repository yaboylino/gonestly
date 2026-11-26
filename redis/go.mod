module github.com/yaboylino/gonestly/redis

go 1.26.0

toolchain go1.26.11

require github.com/redis/go-redis/v9 v9.24.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/yaboylino/gonestly v0.3.0 // indirect
	github.com/yaboylino/gonestly/cache v0.3.0
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
)

// In this repository the module is developed against the framework next to it; a project that
// requires this module uses the released framework.
replace (
	github.com/yaboylino/gonestly => ../
	github.com/yaboylino/gonestly/cache => ../cache
)
