package jobs

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yaboylino/gonestly/sql"
)

// Status is where a job is in its life.
type Status string

// The statuses a job has.
const (
	Pending Status = "pending"
	Running Status = "running"
	Done    Status = "done"
	// Failed jobs ran out of attempts; Retry puts one back.
	Failed Status = "failed"
)

// record is a row of the jobs table.
type record struct {
	ID          int64   `db:"id"`
	Queue       string  `db:"queue"`
	Name        string  `db:"name"`
	Payload     string  `db:"payload"`
	Status      string  `db:"status"`
	Priority    int     `db:"priority"`
	Attempts    int     `db:"attempts"`
	MaxAttempts int     `db:"max_attempts"`
	RunAt       int64   `db:"run_at"`
	LockedBy    *string `db:"locked_by"`
	LockedUntil *int64  `db:"locked_until"`
	LastError   *string `db:"last_error"`
	UniqueKey   *string `db:"unique_key"`
	RequestID   *string `db:"request_id"`
	CreatedAt   int64   `db:"created_at"`
	FinishedAt  *int64  `db:"finished_at"`
}

func (record) TableName() string { return Table }

// Queue stores jobs and, once the application has started, runs them. Providers receive it by
// injection and register their jobs with Define.
type Queue struct {
	db     *sql.DB
	repo   *sql.Repository[record, int64]
	opts   Options
	logger *slog.Logger
	now    func() time.Time

	mu        sync.RWMutex
	handlers  map[string]*handler
	schedules []*scheduled

	wake     chan struct{}
	claims   atomic.Int64
	started  bool
	stopLoop context.CancelFunc
	stopJobs context.CancelFunc
	jobCtx   context.Context
	loops    sync.WaitGroup
	running  sync.WaitGroup
}

type handler struct {
	name     string
	settings settings
	run      func(ctx context.Context, payload []byte) error
}

// New creates a Queue on a database whose jobs table exists. The Queue works after OnStart.
func New(db *sql.DB, opts Options) (*Queue, error) {
	if db == nil {
		return nil, errors.New("jobs: the database is nil")
	}
	repo, err := sql.NewRepository[record, int64](db)
	if err != nil {
		return nil, err
	}
	if opts.Concurrency < 0 || opts.PollInterval < 0 || opts.Retention < 0 || opts.FailedRetention < 0 {
		return nil, errors.New("jobs: Concurrency, PollInterval, Retention and FailedRetention cannot be negative")
	}
	if opts.Concurrency == 0 {
		opts.Concurrency = 4
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if opts.Retention == 0 {
		opts.Retention = 7 * 24 * time.Hour
	}
	if opts.FailedRetention == 0 {
		opts.FailedRetention = 30 * 24 * time.Hour
	}
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.WorkerID == "" {
		opts.WorkerID = workerID()
	}
	return &Queue{
		db: db, repo: repo, opts: opts, logger: opts.Logger.With(slog.String("component", "jobs")),
		now:      time.Now,
		handlers: map[string]*handler{},
		wake:     make(chan struct{}, 1),
	}, nil
}

func workerID() string {
	host, _ := os.Hostname()
	suffix := make([]byte, 3)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("%s-%d-%s", host, os.Getpid(), hex.EncodeToString(suffix))
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// OnStart starts the worker, the scheduler and the cleanup, unless Options.EnqueueOnly is set.
func (q *Queue) OnStart(context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started || q.opts.EnqueueOnly {
		return nil
	}
	q.started = true
	loopCtx, stopLoop := context.WithCancel(context.Background())
	q.jobCtx, q.stopJobs = context.WithCancel(context.Background())
	q.stopLoop = stopLoop
	q.loops.Add(3)
	go func() { defer q.loops.Done(); q.workLoop(loopCtx) }()
	go func() { defer q.loops.Done(); q.scheduleLoop(loopCtx) }()
	go func() { defer q.loops.Done(); q.purgeLoop(loopCtx) }()
	return nil
}

// OnStop stops taking new jobs and waits for the running ones until ctx ends; the jobs that are
// still running then are canceled and put back, to run again in another process.
func (q *Queue) OnStop(ctx context.Context) error {
	q.mu.Lock()
	started := q.started
	q.started = false
	q.mu.Unlock()
	if !started {
		return nil
	}
	q.stopLoop()
	q.loops.Wait()
	done := make(chan struct{})
	go func() { q.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		q.logger.Warn("stopping with jobs still running; they are put back")
		q.stopJobs()
		<-done
	}
	q.stopJobs()
	return nil
}

func (q *Queue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Queue) workLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	slots := make(chan struct{}, q.opts.Concurrency)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-q.wake:
		}
		for ctx.Err() == nil {
			free := q.opts.Concurrency - len(slots)
			if free == 0 {
				break
			}
			claimed, err := q.claim(ctx, free)
			if err != nil {
				if ctx.Err() == nil {
					q.logger.Error("claiming jobs", slog.Any("error", err))
				}
				break
			}
			for _, job := range claimed {
				slots <- struct{}{}
				q.running.Add(1)
				go func() {
					defer func() { <-slots; q.running.Done(); q.signal() }()
					q.execute(q.jobCtx, job)
				}()
			}
			if len(claimed) < free {
				break
			}
		}
		timer.Reset(q.opts.PollInterval)
	}
}

// names returns the registered jobs this process works on.
func (q *Queue) names() []string {
	q.mu.RLock()
	defer q.mu.RUnlock()
	var names []string
	for name, h := range q.handlers {
		if len(q.opts.Queues) == 0 || contains(q.opts.Queues, h.settings.queue) {
			names = append(names, name)
		}
	}
	return names
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

const (
	dueRows = " AND ((status = 'pending' AND run_at <= ?) OR (status = 'running' AND locked_until < ?)) ORDER BY priority DESC, run_at, id LIMIT ?"
	// lock keeps concurrent workers from taking the same rows.
	lock = " FOR UPDATE SKIP LOCKED"
)

// claimOneStatement is claim for SQLite, which has one writer at a time: reading and then
// writing in a transaction fails with SQLITE_BUSY when another process writes in between, so
// the claim is a single UPDATE that returns the rows it took.
func (q *Queue) claimOneStatement(ctx context.Context, names []string, limit int) ([]record, error) {
	now := q.now()
	by := fmt.Sprintf("%s/%d", q.opts.WorkerID, q.claims.Add(1))
	args := []any{by}
	var cases strings.Builder
	for _, name := range names {
		cases.WriteString(" WHEN ? THEN ?")
		args = append(args, name, ms(now.Add(q.handler(name).settings.timeout+lockGrace)))
	}
	for _, name := range names {
		args = append(args, name)
	}
	args = append(args, ms(now), ms(now), limit)
	claimed, err := sql.Query[record](ctx, q.db,
		"UPDATE "+Table+" SET status = 'running', attempts = attempts + 1, locked_by = ?, locked_until = CASE name"+cases.String()+" END"+
			" WHERE id IN (SELECT id FROM "+Table+" WHERE name IN ("+placeholders(len(names))+")"+dueRows+") RETURNING *", args...)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(claimed, func(a, b record) int {
		return cmp.Or(cmp.Compare(b.Priority, a.Priority), cmp.Compare(a.RunAt, b.RunAt), cmp.Compare(a.ID, b.ID))
	})
	return claimed, nil
}

// claim takes up to limit due jobs for this process.
func (q *Queue) claim(ctx context.Context, limit int) ([]record, error) {
	names := q.names()
	if len(names) == 0 || limit < 1 {
		return nil, nil
	}
	if q.db.Dialect().Name() == sql.SQLite.Name() {
		return q.claimOneStatement(ctx, names, limit)
	}
	var claimed []record
	err := q.db.Transaction(ctx, func(ctx context.Context) error {
		now := ms(q.now())
		args := []any{}
		for _, name := range names {
			args = append(args, name)
		}
		args = append(args, now, now, limit)
		due, err := sql.Query[record](ctx, q.db,
			"SELECT * FROM "+Table+" WHERE name IN ("+placeholders(len(names))+")"+dueRows+lock, args...)
		if err != nil {
			return err
		}
		claimed = claimed[:0]
		for _, job := range due {
			q.mu.RLock()
			h := q.handlers[job.Name]
			q.mu.RUnlock()
			if h == nil {
				continue
			}
			until := ms(q.now().Add(h.settings.timeout + lockGrace))
			by := fmt.Sprintf("%s/%d", q.opts.WorkerID, q.claims.Add(1))
			if _, err := q.db.Exec(ctx, "UPDATE "+Table+" SET status = 'running', locked_by = ?, locked_until = ?, attempts = attempts + 1 WHERE id = ?", by, until, job.ID); err != nil {
				return err
			}
			job.Status, job.LockedBy, job.LockedUntil, job.Attempts = string(Running), &by, &until, job.Attempts+1
			claimed = append(claimed, job)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}
