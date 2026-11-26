// Package redis is a cache.Store on Redis, so that every instance of an application shares one
// cache.
//
//	store, err := redis.Open("redis://localhost:6379/0")
//	c, err := cache.New(cache.Options{Store: store, Prefix: "shop:"})
//
// The store is the cache of Gonestly's cache module; use the same Redis for several applications
// by giving each cache its own Prefix.
package redis
