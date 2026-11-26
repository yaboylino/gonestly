package jobs

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"
)

// Options configures a Queue. The zero value works.
type Options struct {
	// Concurrency is the number of jobs that run at the same time in this process; 4 when zero.
	Concurrency int
	// PollInterval is how often the database is asked for due jobs; 1 second when zero. A job
	// enqueued by this process is picked up at once.
	PollInterval time.Duration
	// Queues limits the queues this process works on; all of them when empty.
	Queues []string
	// EnqueueOnly turns off running and scheduling in this process, for instances that only
	// create jobs.
	EnqueueOnly bool
	// Retention is how long finished jobs are kept; 7 days when zero.
	Retention time.Duration
	// FailedRetention is how long jobs that ran out of attempts are kept for inspection and
	// Retry; 30 days when zero.
	FailedRetention time.Duration
	// Location is the time zone of cron expressions; UTC when nil.
	Location *time.Location
	// WorkerID names this process in the table; the host name, process id and a random part when empty.
	WorkerID string
	// Logger receives what the worker does; Module uses the application's logger.
	Logger *slog.Logger
}

// Option changes how a job is defined or enqueued. Each option works at one of the two:
// Define takes MaxAttempts, Timeout, OnQueue, Priority and Backoff, which become the defaults
// of every run, and Enqueue takes Delay, At, Unique, Priority and MaxAttempts for one run.
type Option func(*settings)

type settings struct {
	maxAttempts int
	timeout     time.Duration
	queue       string
	priority    int
	backoff     func(attempt int) time.Duration
	runAt       time.Time
	delay       time.Duration
	unique      string
	defineOnly  []string
	enqueueOnly []string
}

// MaxAttempts is the number of times a job runs before it is given up on; 3 by default.
func MaxAttempts(n int) Option { return func(s *settings) { s.maxAttempts = n } }

// Timeout stops a run after this long, by canceling its context; 5 minutes by default.
func Timeout(d time.Duration) Option {
	return func(s *settings) { s.timeout = d; s.defineOnly = append(s.defineOnly, "Timeout") }
}

// OnQueue puts the job on a named queue, so a process can work on some queues only; "default".
func OnQueue(name string) Option {
	return func(s *settings) { s.queue = name; s.defineOnly = append(s.defineOnly, "OnQueue") }
}

// Priority orders due jobs: a higher number runs first. 0 by default.
func Priority(n int) Option { return func(s *settings) { s.priority = n } }

// Backoff decides how long to wait before the next attempt, given the number of the attempt
// that failed. The default doubles from 10 seconds up to an hour, with some jitter.
func Backoff(fn func(attempt int) time.Duration) Option {
	return func(s *settings) { s.backoff = fn; s.defineOnly = append(s.defineOnly, "Backoff") }
}

// Delay makes a job wait this long before it can run.
func Delay(d time.Duration) Option {
	return func(s *settings) { s.delay = d; s.enqueueOnly = append(s.enqueueOnly, "Delay") }
}

// At makes a job wait until this moment.
func At(t time.Time) Option {
	return func(s *settings) { s.runAt = t; s.enqueueOnly = append(s.enqueueOnly, "At") }
}

// Unique allows one job with this key at a time: enqueueing another while the first is still in
// the table returns ErrDuplicate. A job leaves the table when its retention has passed.
func Unique(key string) Option {
	return func(s *settings) { s.unique = key; s.enqueueOnly = append(s.enqueueOnly, "Unique") }
}

func apply(opts []Option, base settings) settings {
	s := base
	s.defineOnly, s.enqueueOnly = nil, nil
	for _, opt := range opts {
		opt(&s)
	}
	return s
}

// checkSite rejects an option that does not work where it is used.
func (s settings) checkSite(define bool) error {
	wrong, where := s.enqueueOnly, "Define"
	if !define {
		wrong, where = s.defineOnly, "Enqueue"
	}
	if len(wrong) > 0 {
		return fmt.Errorf("jobs: %s does not take %s", where, wrong[0])
	}
	return nil
}

const (
	defaultMaxAttempts = 3
	defaultTimeout     = 5 * time.Minute
	defaultQueue       = "default"
	// lockGrace is added to the timeout of a run, before another worker may take the job over.
	lockGrace = 30 * time.Second
)

func defaultBackoff(attempt int) time.Duration {
	d := 10 * time.Second << min(max(attempt-1, 0), 9)
	d = min(d, time.Hour)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) //nolint:gosec // jitter does not need a secure source
}

// ErrDuplicate is returned by Enqueue when a job with the same Unique key exists.
var ErrDuplicate = errors.New("jobs: a job with this key exists")

// ErrNotFound is returned by Retry when there is no failed job with that id.
var ErrNotFound = errors.New("jobs: no failed job with this id")

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks an error as one that a retry cannot fix, so the job fails at once.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}
