package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"
)

// Job is a defined job with the type of its payload. Define makes one.
type Job[T any] struct {
	queue *Queue
	name  string
	set   settings
}

// Define registers fn as the handler of the job called name, and returns the Job to enqueue and
// schedule it with. The payload is stored as JSON. Handlers must be idempotent: a job runs at
// least once, and runs again when a process dies halfway or the result cannot be recorded.
//
// Define the job in the constructor of the provider that owns the handler, so it is registered
// before the application starts. A name can be defined once.
func Define[T any](q *Queue, name string, fn func(ctx context.Context, payload T) error, opts ...Option) (*Job[T], error) {
	switch {
	case q == nil:
		return nil, errors.New("jobs: the queue is nil")
	case name == "" || len(name) > 200:
		return nil, errors.New("jobs: a job needs a name of 1 to 200 characters")
	case fn == nil:
		return nil, fmt.Errorf("jobs: the job %q has no handler", name)
	}
	s := apply(opts, settings{maxAttempts: defaultMaxAttempts, timeout: defaultTimeout, queue: defaultQueue, backoff: defaultBackoff})
	if err := s.checkSite(true); err != nil {
		return nil, err
	}
	switch {
	case s.maxAttempts < 1:
		return nil, fmt.Errorf("jobs: the job %q needs at least one attempt", name)
	case s.timeout <= 0:
		return nil, fmt.Errorf("jobs: the job %q needs a timeout above zero", name)
	case s.queue == "" || len(s.queue) > 100:
		return nil, fmt.Errorf("jobs: the queue of %q needs a name of 1 to 100 characters", name)
	case s.backoff == nil:
		s.backoff = defaultBackoff
	}
	run := func(ctx context.Context, data []byte) error {
		var payload T
		if err := json.Unmarshal(data, &payload); err != nil {
			return Permanent(fmt.Errorf("the payload does not fit %T: %w", payload, err))
		}
		return fn(ctx, payload)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.handlers[name]; ok {
		return nil, fmt.Errorf("jobs: the job %q is defined twice", name)
	}
	q.handlers[name] = &handler{name: name, settings: s, run: run}
	return &Job[T]{queue: q, name: name, set: s}, nil
}

// Enqueue stores a job to run as soon as a worker is free, or later with Delay or At. It uses
// the transaction in ctx, so a job enqueued in a transaction exists only if that commits. It
// returns the id of the job, or ErrDuplicate when Unique names a job that exists.
func (j *Job[T]) Enqueue(ctx context.Context, payload T, opts ...Option) (int64, error) {
	s := apply(opts, j.set)
	if err := s.checkSite(false); err != nil {
		return 0, err
	}
	if s.maxAttempts < 1 {
		return 0, fmt.Errorf("jobs: the job %q needs at least one attempt", j.name)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("jobs: encoding the payload of %q: %w", j.name, err)
	}
	now := j.queue.now()
	runAt := now
	if !s.runAt.IsZero() {
		runAt = s.runAt
	}
	rec := record{
		Queue: j.set.queue, Name: j.name, Payload: string(data), Status: string(Pending), Priority: s.priority,
		MaxAttempts: s.maxAttempts, RunAt: ms(runAt.Add(s.delay)), CreatedAt: ms(now),
	}
	if id := gonestly.RequestIDFrom(ctx); id != "" {
		rec.RequestID = &id
	}
	if s.unique != "" {
		if len(s.unique) > 255 {
			return 0, errors.New("jobs: a unique key has at most 255 characters")
		}
		rec.UniqueKey = &s.unique
	}
	if err := j.queue.repo.Create(ctx, &rec); err != nil {
		if s.unique != "" && errors.Is(err, sql.ErrConflict) {
			return 0, ErrDuplicate
		}
		return 0, fmt.Errorf("jobs: enqueueing %q: %w", j.name, err)
	}
	j.queue.signal()
	return rec.ID, nil
}

// scheduled is a job that is enqueued on a schedule.
type scheduled struct {
	name    string
	spec    string
	when    *schedule
	enqueue func(ctx context.Context, fire time.Time) error
	nextAt  time.Time
}

// Schedule enqueues the job with this payload on a cron schedule: five fields (minute, hour,
// day of the month, month, day of the week), @hourly, @daily, @weekly, @monthly, @yearly or
// "@every 5m". The time zone is Options.Location.
//
// Every process that schedules the job tries to enqueue each run, and only one succeeds, so a
// job that is scheduled in every instance still runs once per moment. A run that is missed
// because every process was down is not made up. Schedule before the application starts.
func (j *Job[T]) Schedule(spec string, payload T, opts ...Option) error {
	when, err := parseSchedule(spec, j.queue.opts.Location)
	if err != nil {
		return err
	}
	if err := apply(opts, j.set).checkSite(false); err != nil {
		return err
	}
	enqueue := func(ctx context.Context, fire time.Time) error {
		key := fmt.Sprintf("cron:%s:%s:%d", j.name, spec, fire.Unix())
		_, err := j.Enqueue(ctx, payload, append(opts[:len(opts):len(opts)], Unique(key))...)
		if errors.Is(err, ErrDuplicate) {
			return nil
		}
		return err
	}
	j.queue.mu.Lock()
	defer j.queue.mu.Unlock()
	j.queue.schedules = append(j.queue.schedules, &scheduled{name: j.name, spec: spec, when: when, enqueue: enqueue})
	return nil
}

// scheduleLoop enqueues the scheduled jobs when they are due.
func (q *Queue) scheduleLoop(ctx context.Context) {
	for {
		now := q.now()
		wait := q.opts.PollInterval
		q.mu.RLock()
		schedules := append([]*scheduled(nil), q.schedules...)
		q.mu.RUnlock()
		for _, s := range schedules {
			if s.nextAt.IsZero() {
				s.nextAt = s.when.next(now)
			}
			if !s.nextAt.After(now) {
				if err := s.enqueue(ctx, s.nextAt); err != nil && ctx.Err() == nil {
					q.logger.Error("enqueueing a scheduled job", slog.String("job", s.name), slog.String("schedule", s.spec), slog.Any("error", err))
				}
				s.nextAt = s.when.next(now)
			}
			if until := s.nextAt.Sub(now); !s.nextAt.IsZero() && until < wait {
				wait = until
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(max(wait, 10*time.Millisecond)):
		}
	}
}
