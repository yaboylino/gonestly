package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/yaboylino/gonestly"
	"github.com/yaboylino/gonestly/sql"
)

// Info describes the run that a handler is part of.
type Info struct {
	ID          int64
	Name        string
	Attempt     int
	MaxAttempts int
}

type infoKey struct{}

// Current returns the job that ctx belongs to, inside a handler.
func Current(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(infoKey{}).(Info)
	return info, ok
}

// execute runs a claimed job and records how it went.
func (q *Queue) execute(ctx context.Context, job record) {
	q.mu.RLock()
	h := q.handlers[job.Name]
	q.mu.RUnlock()
	logger := q.logger.With(slog.Int64("job_id", job.ID), slog.String("job", job.Name), slog.Int("attempt", job.Attempts))

	if job.Attempts > job.MaxAttempts {
		q.finish(job, errors.New("the worker stopped before the job finished, on every attempt"), true, logger)
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, h.settings.timeout)
	defer cancel()
	if job.RequestID != nil {
		// The job continues the request that enqueued it, so its logs can be followed from there.
		runCtx = gonestly.WithRequestID(runCtx, *job.RequestID)
		logger = logger.With(slog.String("request_id", *job.RequestID))
	}
	runCtx = context.WithValue(runCtx, infoKey{}, Info{ID: job.ID, Name: job.Name, Attempt: job.Attempts, MaxAttempts: job.MaxAttempts})

	start := q.now()
	err := call(runCtx, h, []byte(job.Payload))
	switch {
	case err == nil:
		logger.Info("job done", slog.Duration("took", q.now().Sub(start)))
		q.finish(job, nil, false, logger)
	case ctx.Err() != nil:
		q.release(job, logger)
	default:
		var permanent *permanentError
		q.finish(job, err, errors.As(err, &permanent) || job.Attempts >= job.MaxAttempts, logger)
	}
}

// call runs the handler and turns a panic into an error.
func call(ctx context.Context, h *handler, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	return h.run(ctx, payload)
}

// finish records the outcome of a run in the row, when this process still holds the job.
func (q *Queue) finish(job record, runErr error, final bool, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := ms(q.now())
	var (
		result string
		args   []any
	)
	switch {
	case runErr == nil:
		result = "UPDATE " + Table + " SET status = 'done', finished_at = ?, locked_by = NULL, locked_until = NULL, last_error = NULL WHERE id = ? AND locked_by = ?"
		args = []any{now, job.ID, *job.LockedBy}
	case final:
		logger.Error("job failed for good", slog.Any("error", runErr))
		result = "UPDATE " + Table + " SET status = 'failed', finished_at = ?, locked_by = NULL, locked_until = NULL, last_error = ? WHERE id = ? AND locked_by = ?"
		args = []any{now, runErr.Error(), job.ID, *job.LockedBy}
	default:
		h := q.handler(job.Name)
		wait := h.settings.backoff(job.Attempts)
		logger.Warn("job failed, trying again", slog.Any("error", runErr), slog.Duration("in", wait))
		result = "UPDATE " + Table + " SET status = 'pending', run_at = ?, locked_by = NULL, locked_until = NULL, last_error = ? WHERE id = ? AND locked_by = ?"
		args = []any{ms(q.now().Add(wait)), runErr.Error(), job.ID, *job.LockedBy}
	}
	res, err := q.db.Exec(ctx, result, args...)
	if err != nil {
		logger.Error("recording the result of a job; it runs again when its lock expires", slog.Any("error", err))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		logger.Warn("the job was taken over by another process before it finished")
	}
}

// release puts a job back that was cut off by a shutdown, without using up an attempt.
func (q *Queue) release(job record, logger *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := q.db.Exec(ctx, "UPDATE "+Table+" SET status = 'pending', attempts = attempts - 1, locked_by = NULL, locked_until = NULL WHERE id = ? AND locked_by = ?", job.ID, *job.LockedBy); err != nil {
		logger.Error("putting a job back", slog.Any("error", err))
	}
}

func (q *Queue) handler(name string) *handler {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.handlers[name]
}

// RunDue runs every job that is due, in this process and one after the other, and returns how
// many ran. It is meant for tests, which enqueue jobs and then want their effects at once.
func (q *Queue) RunDue(ctx context.Context) (int, error) {
	total := 0
	for {
		jobs, err := q.claim(ctx, 100)
		if err != nil || len(jobs) == 0 {
			return total, err
		}
		for _, job := range jobs {
			q.execute(ctx, job)
			total++
		}
	}
}

// Stats counts the jobs per status.
type Stats struct {
	Pending, Running, Done, Failed int64
}

// Stats counts the jobs per status.
func (q *Queue) Stats(ctx context.Context) (Stats, error) {
	rows, err := sql.Query[struct {
		Status string `db:"status"`
		Count  int64  `db:"count"`
	}](ctx, q.db, "SELECT status, COUNT(*) AS count FROM "+Table+" GROUP BY status")
	if err != nil {
		return Stats{}, err
	}
	var s Stats
	for _, row := range rows {
		switch Status(row.Status) {
		case Pending:
			s.Pending = row.Count
		case Running:
			s.Running = row.Count
		case Done:
			s.Done = row.Count
		case Failed:
			s.Failed = row.Count
		}
	}
	return s, nil
}

// FailedJob is a job that ran out of attempts.
type FailedJob struct {
	ID         int64
	Name       string
	Queue      string
	Payload    string
	Attempts   int
	Error      string
	FinishedAt time.Time
}

// Failed lists the jobs that ran out of attempts, most recent first.
func (q *Queue) Failed(ctx context.Context, limit int) ([]FailedJob, error) {
	rows, err := sql.Query[record](ctx, q.db, "SELECT * FROM "+Table+" WHERE status = 'failed' ORDER BY finished_at DESC, id DESC LIMIT ?", max(limit, 1))
	if err != nil {
		return nil, err
	}
	failed := make([]FailedJob, len(rows))
	for i, r := range rows {
		failed[i] = FailedJob{ID: r.ID, Name: r.Name, Queue: r.Queue, Payload: r.Payload, Attempts: r.Attempts}
		if r.LastError != nil {
			failed[i].Error = *r.LastError
		}
		if r.FinishedAt != nil {
			failed[i].FinishedAt = time.UnixMilli(*r.FinishedAt)
		}
	}
	return failed, nil
}

// Retry gives a failed job a fresh set of attempts, starting now. It returns ErrNotFound when
// no failed job has the id.
func (q *Queue) Retry(ctx context.Context, id int64) error {
	res, err := q.db.Exec(ctx, "UPDATE "+Table+" SET status = 'pending', attempts = 0, run_at = ?, finished_at = NULL WHERE id = ? AND status = 'failed'", ms(q.now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	q.signal()
	return nil
}

func (q *Queue) purgeLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		q.purge(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// purge removes finished and given up jobs after their retention.
func (q *Queue) purge(ctx context.Context) {
	now := q.now()
	for status, keep := range map[Status]time.Duration{Done: q.opts.Retention, Failed: q.opts.FailedRetention} {
		if _, err := q.db.Exec(ctx, "DELETE FROM "+Table+" WHERE status = ? AND finished_at < ?", string(status), ms(now.Add(-keep))); err != nil && ctx.Err() == nil {
			q.logger.Error("removing old jobs", slog.Any("error", err))
		}
	}
}
