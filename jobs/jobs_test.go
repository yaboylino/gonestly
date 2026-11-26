package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yaboylino/gonestly/sql"
)

type greeting struct {
	Name string `json:"name"`
}

func TestEnqueueAndRun(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		var got []string
		job, err := Define(q, "greet", func(ctx context.Context, g greeting) error {
			info, ok := Current(ctx)
			if !ok || info.Name != "greet" || info.Attempt != 1 || info.MaxAttempts != 3 {
				t.Errorf("Current = %+v, %v", info, ok)
			}
			got = append(got, g.Name)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		id, err := job.Enqueue(t.Context(), greeting{Name: "Ada"})
		if err != nil || id == 0 {
			t.Fatalf("Enqueue = %d, %v", id, err)
		}
		if n, err := q.RunDue(t.Context()); err != nil || n != 1 {
			t.Fatalf("RunDue = %d, %v", n, err)
		}
		if len(got) != 1 || got[0] != "Ada" {
			t.Errorf("handled %v", got)
		}
		if r := status(t, db, id); r.Status != "done" || r.FinishedAt == nil || r.LockedBy != nil || r.Attempts != 1 {
			t.Errorf("row = %+v", r)
		}
		if s, err := q.Stats(t.Context()); err != nil || s.Done != 1 || s.Pending != 0 {
			t.Errorf("Stats = %+v, %v", s, err)
		}
	})
}

func TestRetriesUntilItWorks(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		calls := 0
		job, _ := Define(q, "flaky", func(context.Context, greeting) error {
			calls++
			if calls < 3 {
				return fmt.Errorf("try %d failed", calls)
			}
			return nil
		}, Backoff(noWait))
		id, _ := job.Enqueue(t.Context(), greeting{})
		if _, err := q.RunDue(t.Context()); err != nil {
			t.Fatal(err)
		}
		if r := status(t, db, id); calls != 3 || r.Status != "done" || r.Attempts != 3 {
			t.Errorf("calls = %d, row = %+v", calls, r)
		}
	})
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		calls := 0
		job, _ := Define(q, "broken", func(context.Context, greeting) error {
			calls++
			return errors.New("always broken")
		}, Backoff(noWait), MaxAttempts(2))
		id, _ := job.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		r := status(t, db, id)
		if calls != 2 || r.Status != "failed" || r.LastError == nil || !strings.Contains(*r.LastError, "always broken") {
			t.Errorf("calls = %d, row = %+v", calls, r)
		}
		failed, err := q.Failed(t.Context(), 10)
		if err != nil || len(failed) != 1 || failed[0].ID != id || failed[0].Attempts != 2 {
			t.Errorf("Failed = %+v, %v", failed, err)
		}

		// Retry gives a failed job a fresh set of attempts.
		if err := q.Retry(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		_, _ = q.RunDue(t.Context())
		if calls != 4 {
			t.Errorf("calls after Retry = %d, want 4", calls)
		}
		if err := q.Retry(t.Context(), 999999); !errors.Is(err, ErrNotFound) {
			t.Errorf("Retry of nothing = %v", err)
		}
		if err := q.Retry(t.Context(), id+0); err != nil && !errors.Is(err, ErrNotFound) {
			t.Error(err)
		}
	})
}

func TestPermanentErrorAndPanic(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		permanent := 0
		a, _ := Define(q, "permanent", func(context.Context, greeting) error {
			permanent++
			return Permanent(errors.New("no point in trying again"))
		}, Backoff(noWait))
		panics := 0
		b, _ := Define(q, "panics", func(context.Context, greeting) error {
			panics++
			if panics == 1 {
				panic("boom")
			}
			return nil
		}, Backoff(noWait))
		idA, _ := a.Enqueue(t.Context(), greeting{})
		idB, _ := b.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		if r := status(t, db, idA); permanent != 1 || r.Status != "failed" {
			t.Errorf("permanent: calls = %d, row = %+v", permanent, r)
		}
		if r := status(t, db, idB); panics != 2 || r.Status != "done" {
			t.Errorf("panic: calls = %d, row = %+v", panics, r)
		}
	})
}

func TestBadPayloadFailsAtOnce(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		_, _ = Define(q, "typed", func(context.Context, greeting) error { return nil }, Backoff(noWait))
		mustExec(t, db, "INSERT INTO "+Table+" (name, payload, status, max_attempts, run_at, created_at) VALUES ('typed', '[1]', 'pending', 5, 0, 0)")
		_, _ = q.RunDue(t.Context())
		s, _ := q.Stats(t.Context())
		if s.Failed != 1 {
			t.Errorf("Stats = %+v", s)
		}
	})
}

func TestDelayAndAt(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		c := newClock()
		q.now = c.Now
		runs := 0
		job, _ := Define(q, "later", func(context.Context, greeting) error { runs++; return nil })
		_, _ = job.Enqueue(t.Context(), greeting{}, Delay(time.Minute))
		_, _ = job.Enqueue(t.Context(), greeting{}, At(c.Now().Add(time.Hour)))
		if n, _ := q.RunDue(t.Context()); n != 0 {
			t.Fatalf("ran %d jobs before they were due", n)
		}
		c.Advance(2 * time.Minute)
		if n, _ := q.RunDue(t.Context()); n != 1 {
			t.Fatalf("ran %d jobs after a minute, want 1", n)
		}
		c.Advance(time.Hour)
		if n, _ := q.RunDue(t.Context()); n != 1 || runs != 2 {
			t.Fatalf("ran %d jobs after an hour, runs = %d", n, runs)
		}
	})
}

func TestBackoffDelaysTheRetry(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		c := newClock()
		q.now = c.Now
		calls := 0
		job, _ := Define(q, "slow-retry", func(context.Context, greeting) error { calls++; return errors.New("no") },
			Backoff(func(attempt int) time.Duration { return time.Duration(attempt) * time.Minute }))
		_, _ = job.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		if calls != 1 {
			t.Fatalf("calls = %d", calls)
		}
		c.Advance(30 * time.Second)
		_, _ = q.RunDue(t.Context())
		if calls != 1 {
			t.Errorf("retried before the backoff: %d", calls)
		}
		c.Advance(time.Minute)
		_, _ = q.RunDue(t.Context())
		if calls != 2 {
			t.Errorf("calls after the backoff = %d", calls)
		}
	})
}

func TestUnique(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		job, _ := Define(q, "once", func(context.Context, greeting) error { return nil })
		if _, err := job.Enqueue(t.Context(), greeting{}, Unique("report-1")); err != nil {
			t.Fatal(err)
		}
		if _, err := job.Enqueue(t.Context(), greeting{}, Unique("report-1")); !errors.Is(err, ErrDuplicate) {
			t.Errorf("second Enqueue = %v, want ErrDuplicate", err)
		}
		if _, err := job.Enqueue(t.Context(), greeting{}, Unique("report-2")); err != nil {
			t.Errorf("another key: %v", err)
		}
		if _, err := job.Enqueue(t.Context(), greeting{}); err != nil {
			t.Errorf("without a key: %v", err)
		}
		if _, err := job.Enqueue(t.Context(), greeting{}); err != nil {
			t.Errorf("a second job without a key: %v", err)
		}
	})
}

func TestEnqueueInTransaction(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		job, _ := Define(q, "in-tx", func(context.Context, greeting) error { return nil })
		boom := errors.New("rolled back")
		err := db.Transaction(t.Context(), func(ctx context.Context) error {
			if _, err := job.Enqueue(ctx, greeting{}); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
		if s, _ := q.Stats(t.Context()); s.Pending != 0 {
			t.Errorf("a job of a rolled back transaction exists: %+v", s)
		}
		err = db.Transaction(t.Context(), func(ctx context.Context) error {
			_, err := job.Enqueue(ctx, greeting{})
			return err
		})
		if s, _ := q.Stats(t.Context()); err != nil || s.Pending != 1 {
			t.Errorf("a committed job: %+v, %v", s, err)
		}
	})
}

func TestEveryJobRunsOnceAcrossWorkers(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		const jobsCount = 60
		var mu sync.Mutex
		seen := map[int]int{}
		type number struct{ N int }
		var queues []*Queue
		var enqueue *Job[number]
		for i := range 3 {
			q := newQueue(t, db, Options{Concurrency: 4, PollInterval: 20 * time.Millisecond, WorkerID: fmt.Sprintf("w%d", i)})
			job, err := Define(q, "count", func(_ context.Context, n number) error {
				mu.Lock()
				seen[n.N]++
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				enqueue = job
			}
			queues = append(queues, q)
		}
		for n := range jobsCount {
			if _, err := enqueue.Enqueue(t.Context(), number{n}); err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range queues {
			if err := q.OnStart(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			s, _ := queues[0].Stats(t.Context())
			if s.Done == jobsCount || time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, q := range queues {
			if err := q.OnStop(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) != jobsCount {
			t.Errorf("%d of %d jobs ran", len(seen), jobsCount)
		}
		for n, times := range seen {
			if times != 1 {
				t.Errorf("job %d ran %d times", n, times)
			}
		}
	})
}

func TestTakesOverAfterACrash(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		c := newClock()
		q.now = c.Now
		runs := 0
		job, _ := Define(q, "crashy", func(context.Context, greeting) error { runs++; return nil }, MaxAttempts(2), Timeout(time.Minute))
		id, _ := job.Enqueue(t.Context(), greeting{})
		// Another process claimed the job and died.
		mustExec(t, db, "UPDATE "+Table+" SET status = 'running', attempts = 1, locked_by = 'dead/1', locked_until = ? WHERE id = ?", ms(c.Now().Add(time.Minute)), id)
		if n, _ := q.RunDue(t.Context()); n != 0 {
			t.Fatal("took over a job that is still locked")
		}
		c.Advance(2 * time.Minute)
		if n, _ := q.RunDue(t.Context()); n != 1 || runs != 1 {
			t.Fatalf("took over %d jobs, runs = %d", n, runs)
		}
		if r := status(t, db, id); r.Status != "done" || r.Attempts != 2 {
			t.Errorf("row = %+v", r)
		}

		// A job that took its last attempt down with it is not run again.
		id, _ = job.Enqueue(t.Context(), greeting{})
		mustExec(t, db, "UPDATE "+Table+" SET status = 'running', attempts = 2, locked_by = 'dead/2', locked_until = ? WHERE id = ?", ms(c.Now().Add(-time.Second)), id)
		_, _ = q.RunDue(t.Context())
		if r := status(t, db, id); r.Status != "failed" || runs != 1 {
			t.Errorf("row = %+v, runs = %d", r, runs)
		}
	})
}

func TestOnlyWorksOnItsQueues(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{Queues: []string{"fast"}})
		var ran []string
		fast, _ := Define(q, "fast-job", func(context.Context, greeting) error { ran = append(ran, "fast"); return nil }, OnQueue("fast"))
		slow, _ := Define(q, "slow-job", func(context.Context, greeting) error { ran = append(ran, "slow"); return nil }, OnQueue("slow"))
		_, _ = fast.Enqueue(t.Context(), greeting{})
		_, _ = slow.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		if len(ran) != 1 || ran[0] != "fast" {
			t.Errorf("ran %v", ran)
		}
	})
}

func TestJobsWithoutHandlerWait(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		web := newQueue(t, db, Options{})
		worker := newQueue(t, db, Options{})
		var ran atomic.Int32
		sendWeb, _ := Define(web, "export", func(context.Context, greeting) error { ran.Add(1); return nil })
		_, _ = Define(worker, "other", func(context.Context, greeting) error { return nil })
		_, _ = sendWeb.Enqueue(t.Context(), greeting{})
		if n, _ := worker.RunDue(t.Context()); n != 0 {
			t.Fatal("a process ran a job it has no handler for")
		}
		if n, _ := web.RunDue(t.Context()); n != 1 || ran.Load() != 1 {
			t.Errorf("the owner ran %d", n)
		}
	})
}

func TestStopPutsRunningJobsBack(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{PollInterval: 10 * time.Millisecond})
		started := make(chan struct{})
		job, _ := Define(q, "long", func(ctx context.Context, _ greeting) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
		id, _ := job.Enqueue(t.Context(), greeting{})
		if err := q.OnStart(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatal("the job did not start")
		}
		expired, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		if err := q.OnStop(expired); err != nil {
			t.Fatal(err)
		}
		if r := status(t, db, id); r.Status != "pending" || r.Attempts != 0 || r.LockedBy != nil {
			t.Errorf("row = %+v", r)
		}
	})
}

func TestStopWaitsForRunningJobs(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{PollInterval: 10 * time.Millisecond})
		started := make(chan struct{})
		job, _ := Define(q, "finishes", func(context.Context, greeting) error {
			close(started)
			time.Sleep(200 * time.Millisecond)
			return nil
		})
		id, _ := job.Enqueue(t.Context(), greeting{})
		_ = q.OnStart(t.Context())
		<-started
		if err := q.OnStop(t.Context()); err != nil {
			t.Fatal(err)
		}
		if r := status(t, db, id); r.Status != "done" {
			t.Errorf("row = %+v", r)
		}
	})
}

func TestTimeoutCancelsTheRun(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		job, _ := Define(q, "hangs", func(ctx context.Context, _ greeting) error {
			<-ctx.Done()
			return ctx.Err()
		}, Timeout(50*time.Millisecond), MaxAttempts(1))
		id, _ := job.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		if r := status(t, db, id); r.Status != "failed" || r.LastError == nil || !strings.Contains(*r.LastError, "deadline") {
			t.Errorf("row = %+v", r)
		}
	})
}

func TestPriority(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		var order []string
		job, _ := Define(q, "ordered", func(_ context.Context, g greeting) error { order = append(order, g.Name); return nil })
		_, _ = job.Enqueue(t.Context(), greeting{"low"})
		_, _ = job.Enqueue(t.Context(), greeting{"high"}, Priority(10))
		_, _ = q.RunDue(t.Context())
		if len(order) != 2 || order[0] != "high" {
			t.Errorf("order = %v", order)
		}
	})
}

func TestPurge(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{Retention: time.Hour, FailedRetention: 2 * time.Hour})
		c := newClock()
		q.now = c.Now
		ok, _ := Define(q, "purge-ok", func(context.Context, greeting) error { return nil })
		bad, _ := Define(q, "purge-bad", func(context.Context, greeting) error { return Permanent(errors.New("x")) })
		_, _ = ok.Enqueue(t.Context(), greeting{}, Unique("k"))
		_, _ = bad.Enqueue(t.Context(), greeting{})
		_, _ = q.RunDue(t.Context())
		c.Advance(90 * time.Minute)
		q.purge(t.Context())
		if s, _ := q.Stats(t.Context()); s.Done != 0 || s.Failed != 1 {
			t.Errorf("after 90 minutes: %+v", s)
		}
		// The key is free again once its job is gone.
		if _, err := ok.Enqueue(t.Context(), greeting{}, Unique("k")); err != nil {
			t.Errorf("reusing the key: %v", err)
		}
		c.Advance(time.Hour)
		q.purge(t.Context())
		if s, _ := q.Stats(t.Context()); s.Failed != 0 {
			t.Errorf("after 150 minutes: %+v", s)
		}
	})
}

func TestDefineChecks(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{})
		noop := func(context.Context, greeting) error { return nil }
		if _, err := Define(q, "dup", noop); err != nil {
			t.Fatal(err)
		}
		for name, tt := range map[string]struct {
			name string
			opts []Option
			want string
		}{
			"twice":         {"dup", nil, "defined twice"},
			"empty":         {"", nil, "needs a name"},
			"attempts":      {"a", []Option{MaxAttempts(0)}, "at least one attempt"},
			"timeout":       {"b", []Option{Timeout(0)}, "timeout"},
			"enqueue only":  {"c", []Option{Delay(time.Second)}, "Define does not take Delay"},
			"unique":        {"d", []Option{Unique("x")}, "Define does not take Unique"},
			"queue too big": {"e", []Option{OnQueue(strings.Repeat("q", 101))}, "queue"},
		} {
			if _, err := Define(q, tt.name, noop, tt.opts...); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s: err = %v, want %q", name, err, tt.want)
			}
		}
		job, _ := Define(q, "fine", noop)
		if _, err := job.Enqueue(t.Context(), greeting{}, Timeout(time.Second)); err == nil || !strings.Contains(err.Error(), "Enqueue does not take Timeout") {
			t.Errorf("Enqueue with Timeout: %v", err)
		}
		if _, err := job.Enqueue(t.Context(), greeting{}, MaxAttempts(0)); err == nil {
			t.Error("Enqueue with no attempts")
		}
	})
}

func TestNewChecks(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Error("New without a database")
	}
	databases(t, func(t *testing.T, db *sql.DB) {
		if _, err := New(db, Options{Concurrency: -1}); err == nil {
			t.Error("a negative concurrency")
		}
	})
}

func TestMigration(t *testing.T) {
	for _, dialect := range []string{"postgres", "mysql", "sqlite"} {
		up, down, err := Migration(dialect)
		if err != nil || !strings.Contains(up, "CREATE TABLE "+Table) || !strings.Contains(down, "DROP TABLE "+Table) {
			t.Errorf("%s: %v", dialect, err)
		}
	}
	if _, _, err := Migration("oracle"); err == nil {
		t.Error("an unknown dialect")
	}
}

func TestScheduleEnqueuesOncePerMoment(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		a := newQueue(t, db, Options{})
		b := newQueue(t, db, Options{})
		var jobA, jobB *Job[greeting]
		jobA, _ = Define(a, "nightly", func(context.Context, greeting) error { return nil })
		jobB, _ = Define(b, "nightly", func(context.Context, greeting) error { return nil })
		if err := jobA.Schedule("0 3 * * *", greeting{"a"}); err != nil {
			t.Fatal(err)
		}
		if err := jobB.Schedule("0 3 * * *", greeting{"b"}); err != nil {
			t.Fatal(err)
		}
		fire := time.Date(2026, 1, 6, 3, 0, 0, 0, time.UTC)
		for _, q := range []*Queue{a, b, a} {
			if err := q.schedules[0].enqueue(t.Context(), fire); err != nil {
				t.Fatal(err)
			}
		}
		if s, _ := a.Stats(t.Context()); s.Pending != 1 {
			t.Errorf("a moment was enqueued %d times", s.Pending)
		}
		if err := a.schedules[0].enqueue(t.Context(), fire.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if s, _ := a.Stats(t.Context()); s.Pending != 2 {
			t.Errorf("the next day was not enqueued: %+v", s)
		}
		if err := jobA.Schedule("not a schedule", greeting{}); err == nil {
			t.Error("a bad schedule was accepted")
		}
		if err := jobA.Schedule("@daily", greeting{}, Timeout(time.Second)); err == nil {
			t.Error("a Define option was accepted by Schedule")
		}
	})
}

func TestSchedulerRuns(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{PollInterval: 20 * time.Millisecond})
		var runs atomic.Int32
		job, _ := Define(q, "tick", func(context.Context, greeting) error { runs.Add(1); return nil })
		if err := job.Schedule("@every 1s", greeting{}); err != nil {
			t.Fatal(err)
		}
		if err := q.OnStart(t.Context()); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for runs.Load() < 2 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		_ = q.OnStop(t.Context())
		if runs.Load() < 2 {
			t.Errorf("a job scheduled every second ran %d times", runs.Load())
		}
	})
}

func TestEnqueueOnlyDoesNotRun(t *testing.T) {
	databases(t, func(t *testing.T, db *sql.DB) {
		q := newQueue(t, db, Options{EnqueueOnly: true, PollInterval: 10 * time.Millisecond})
		var runs atomic.Int32
		job, _ := Define(q, "idle", func(context.Context, greeting) error { runs.Add(1); return nil })
		_, _ = job.Enqueue(t.Context(), greeting{})
		_ = q.OnStart(t.Context())
		time.Sleep(150 * time.Millisecond)
		_ = q.OnStop(t.Context())
		if runs.Load() != 0 {
			t.Error("an enqueue only queue ran a job")
		}
	})
}
