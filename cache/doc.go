// Package cache keeps the results of slow work for a while: queries, calls to other services,
// whole responses.
//
// A Cache sits on a Store, which holds bytes under keys with an expiry. The memory store is in
// the box and lives in one process; the redis module has a store that all instances of an
// application share.
//
//	user, err := cache.Remember(ctx, c, "user:7", time.Minute, func(ctx context.Context) (User, error) {
//		return users.Find(ctx, 7)
//	})
//
// Remember loads the value once however many requests ask for it at the same moment, and a
// failing cache never fails a request: the value is loaded as if there were no cache. Responses
// can be cached as a whole with Responses.
package cache
