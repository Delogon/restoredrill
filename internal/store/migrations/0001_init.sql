CREATE TABLE IF NOT EXISTS runs (
    id              TEXT PRIMARY KEY,
    drill           TEXT NOT NULL,
    trigger         TEXT NOT NULL,
    status          TEXT NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    repository      TEXT,
    backend         TEXT,
    snapshot_id     TEXT,
    snapshot_time   INTEGER,
    snapshot_host   TEXT,
    seed            INTEGER,
    restored_bytes  INTEGER,
    restored_files  INTEGER,
    restore_seconds REAL,
    error_kind      TEXT,
    error_message   TEXT,
    kept_path       TEXT,
    config_hash     TEXT
);
CREATE INDEX IF NOT EXISTS idx_runs_drill_started ON runs (drill, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_runs_status ON runs (status);

CREATE TABLE IF NOT EXISTS check_results (
    run_id       TEXT NOT NULL,
    seq          INTEGER NOT NULL,
    name         TEXT,
    type         TEXT,
    phase        TEXT,
    status       TEXT,
    started_at   INTEGER,
    duration_ms  INTEGER,
    message      TEXT,
    details_json TEXT,
    PRIMARY KEY (run_id, seq)
);

CREATE TABLE IF NOT EXISTS run_events (
    run_id  TEXT NOT NULL,
    ts      INTEGER NOT NULL,
    level   TEXT,
    step    TEXT,
    message TEXT
);
CREATE INDEX IF NOT EXISTS idx_events_run ON run_events (run_id, ts);

CREATE TABLE IF NOT EXISTS notifications (
    run_id   TEXT NOT NULL,
    notifier TEXT NOT NULL,
    event    TEXT NOT NULL,
    sent_at  INTEGER NOT NULL,
    ok       INTEGER NOT NULL,
    error    TEXT
);
CREATE INDEX IF NOT EXISTS idx_notifications_run ON notifications (run_id);