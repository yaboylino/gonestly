CREATE TABLE gonestly_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    queue VARCHAR(100) NOT NULL DEFAULT 'default',
    name VARCHAR(200) NOT NULL,
    payload TEXT NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    priority INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    run_at BIGINT NOT NULL,
    locked_by VARCHAR(100),
    locked_until BIGINT,
    last_error TEXT,
    unique_key VARCHAR(255),
    created_at BIGINT NOT NULL,
    finished_at BIGINT
);

CREATE UNIQUE INDEX gonestly_jobs_unique_key ON gonestly_jobs (unique_key);
CREATE INDEX gonestly_jobs_due ON gonestly_jobs (status, run_at);
