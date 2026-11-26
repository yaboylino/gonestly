// Package jobs runs background jobs and scheduled work from the database of the application.
//
// A job is a named function with a payload. Enqueue stores it in the gonestly_jobs table; a
// worker in any instance of the application claims it, runs it, and retries it with a growing
// delay when it fails. Because the jobs live in the database, they survive a restart, run once
// however many instances there are, and can be enqueued in the transaction that makes them
// necessary: when the transaction rolls back, so does the job.
//
//	type Welcome struct{ UserID int64 }
//
//	func NewMailer(q *jobs.Queue) (*Mailer, error) {
//		m := &Mailer{}
//		var err error
//		m.welcome, err = jobs.Define(q, "welcome-mail", m.sendWelcome, jobs.MaxAttempts(5))
//		return m, err
//	}
//
//	_, err := m.welcome.Enqueue(ctx, Welcome{UserID: 7}, jobs.Delay(time.Minute))
//
// Create the table with `gonestly add jobs`, or with Migration.
package jobs
