package gonestly

import "context"

// Starter is implemented by providers that need to run code when the application starts.
//
// OnStart is called after the provider's dependencies have started. Do not block in it: start
// long-running work in a goroutine and stop it in OnStop.
type Starter interface {
	OnStart(ctx context.Context) error
}

// Stopper is implemented by providers that need to release resources when the application stops.
//
// OnStop is called in reverse start order, so a provider stops before its dependencies do.
type Stopper interface {
	OnStop(ctx context.Context) error
}

// Shutdowner lets any provider stop the application; App.Run returns the error it was given.
type Shutdowner interface {
	Shutdown(err error)
}
