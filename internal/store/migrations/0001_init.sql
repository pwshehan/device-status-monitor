-- Groups: the structural home of a device. One group per device, no nesting.
CREATE TABLE groups (
  id                  INTEGER PRIMARY KEY AUTOINCREMENT,
  name                TEXT    NOT NULL UNIQUE,
  description         TEXT    NOT NULL DEFAULT '',
  color               TEXT    NOT NULL DEFAULT '',
  sort_order          INTEGER NOT NULL DEFAULT 0,
  -- Probe defaults for members. NULL = fall through to the global defaults.
  check_interval_sec  INTEGER CHECK(check_interval_sec IS NULL OR check_interval_sec >= 5),
  timeout_sec         INTEGER CHECK(timeout_sec IS NULL OR timeout_sec BETWEEN 1 AND 60),
  failure_threshold   INTEGER CHECK(failure_threshold IS NULL OR failure_threshold >= 1),
  recovery_threshold  INTEGER CHECK(recovery_threshold IS NULL OR recovery_threshold >= 1),
  -- Group-level alerting
  notify              INTEGER NOT NULL DEFAULT 1,
  recipients          TEXT,        -- NULL = use alert.recipients
  paused_until        INTEGER,     -- pause a whole site for maintenance
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE TABLE devices (
  id                    INTEGER PRIMARY KEY AUTOINCREMENT,
  name                  TEXT    NOT NULL,
  ip_address            TEXT    NOT NULL,
  port                  INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
  group_id              INTEGER REFERENCES groups(id) ON DELETE SET NULL,
  -- NULL on any of these four = inherit from the group, then the global default.
  -- Nullable rather than sentinel values, so "inherited" is representable.
  check_interval_sec    INTEGER CHECK(check_interval_sec IS NULL OR check_interval_sec >= 5),
  timeout_sec           INTEGER CHECK(timeout_sec IS NULL OR timeout_sec BETWEEN 1 AND 60),
  failure_threshold     INTEGER CHECK(failure_threshold IS NULL OR failure_threshold >= 1),
  recovery_threshold    INTEGER CHECK(recovery_threshold IS NULL OR recovery_threshold >= 1),
  enabled               INTEGER NOT NULL DEFAULT 1,
  notify                INTEGER NOT NULL DEFAULT 1,
  paused_until          INTEGER,
  tags                  TEXT    NOT NULL DEFAULT '',   -- cross-cutting, comma separated
  status                TEXT    NOT NULL DEFAULT 'UNKNOWN'
                          CHECK(status IN ('UP','DOWN','UNKNOWN')),
  consecutive_failures  INTEGER NOT NULL DEFAULT 0,
  consecutive_successes INTEGER NOT NULL DEFAULT 0,
  first_failure_at      INTEGER,
  last_check_at         INTEGER,
  last_latency_ms       INTEGER,
  last_error            TEXT    NOT NULL DEFAULT '',
  last_status_change_at INTEGER,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE(ip_address, port)
);
CREATE INDEX idx_devices_group ON devices(group_id, name);

-- Raw time series. Pruned to retention.raw_days.
CREATE TABLE heartbeats (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id  INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  status     TEXT    NOT NULL CHECK(status IN ('UP','DOWN')),
  latency_ms INTEGER NOT NULL,
  error_msg  TEXT,
  checked_at INTEGER NOT NULL
);
CREATE INDEX idx_heartbeats_device_time ON heartbeats(device_id, checked_at);

-- Pre-aggregated history, so 90-day views never touch raw rows.
CREATE TABLE rollups_daily (
  device_id      INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  day            TEXT    NOT NULL,          -- 'YYYY-MM-DD' local time
  checks_total   INTEGER NOT NULL,
  checks_up      INTEGER NOT NULL,
  uptime_pct     REAL    NOT NULL,
  avg_latency_ms REAL,
  p95_latency_ms INTEGER,
  downtime_sec   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(device_id, day)
) WITHOUT ROWID;

-- One row per outage episode. Source of truth for reported downtime.
CREATE TABLE incidents (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id     INTEGER NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  started_at    INTEGER NOT NULL,   -- first failed probe, not the threshold trip
  detected_at   INTEGER NOT NULL,
  resolved_at   INTEGER,
  duration_sec  INTEGER,
  cause         TEXT    NOT NULL DEFAULT '',
  alert_sent    INTEGER NOT NULL DEFAULT 0,
  recovery_sent INTEGER NOT NULL DEFAULT 0,
  last_alert_at INTEGER             -- for reminder re-sends
);
CREATE INDEX idx_incidents_open ON incidents(device_id) WHERE resolved_at IS NULL;
CREATE INDEX idx_incidents_device_time ON incidents(device_id, started_at);

-- Durable email queue: the alert about a network outage cannot be sent over
-- that outage. Queue it and retry instead of losing it.
CREATE TABLE alert_outbox (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  incident_id     INTEGER REFERENCES incidents(id) ON DELETE SET NULL,
  kind            TEXT    NOT NULL CHECK(kind IN ('DOWN','RECOVERY','TEST','DIGEST')),
  subject         TEXT    NOT NULL,
  body_text       TEXT    NOT NULL,
  body_html       TEXT,
  recipients      TEXT    NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  last_error      TEXT    NOT NULL DEFAULT '',
  created_at      INTEGER NOT NULL,
  sent_at         INTEGER
);
CREATE INDEX idx_outbox_pending ON alert_outbox(next_attempt_at) WHERE sent_at IS NULL;

CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
