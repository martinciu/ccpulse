CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY,
  session_id TEXT NOT NULL,
  message_id TEXT NOT NULL,
  project_slug TEXT NOT NULL,
  ts TEXT NOT NULL,
  role TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  cache_read_tokens INTEGER NOT NULL,
  cache_write_5m_tokens INTEGER NOT NULL,
  cache_write_1h_tokens INTEGER NOT NULL,
  cost_usd_estimate REAL NOT NULL,
  pricing_version TEXT NOT NULL,
  pricing_unknown INTEGER NOT NULL DEFAULT 0,
  is_subagent INTEGER NOT NULL DEFAULT 0,
  parent_session_id TEXT,
  cwd TEXT NOT NULL DEFAULT '',
  git_branch TEXT NOT NULL DEFAULT '',
  repo_root TEXT NOT NULL DEFAULT '',
  UNIQUE(session_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_messages_ts ON messages(ts);
CREATE INDEX IF NOT EXISTS idx_messages_session_ts ON messages(session_id, ts);

CREATE TABLE IF NOT EXISTS files (
  path TEXT PRIMARY KEY,
  mtime_ns INTEGER NOT NULL,
  last_offset_bytes INTEGER NOT NULL,
  last_line INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_samples (
  ts                              INTEGER PRIMARY KEY,           -- Unix epoch seconds; one row per fetch (INSERT OR IGNORE on collision)
  source                          TEXT NOT NULL DEFAULT 'api',   -- always 'api' today; reserved

  five_hour_pct                   REAL,
  five_hour_resets_at             TEXT,

  seven_day_pct                   REAL,
  seven_day_resets_at             TEXT,

  seven_day_sonnet_pct            REAL,
  seven_day_sonnet_resets_at      TEXT,

  seven_day_opus_pct              REAL,
  seven_day_opus_resets_at        TEXT,

  seven_day_omelette_pct          REAL,
  seven_day_omelette_resets_at    TEXT,

  seven_day_oauth_apps_pct        REAL,
  seven_day_oauth_apps_resets_at  TEXT,

  seven_day_cowork_pct            REAL,
  seven_day_cowork_resets_at      TEXT,

  tangelo_pct                     REAL,
  tangelo_resets_at               TEXT,

  iguana_necktie_pct              REAL,
  iguana_necktie_resets_at        TEXT,

  omelette_promotional_pct        REAL,
  omelette_promotional_resets_at  TEXT,

  extra_usage_enabled             INTEGER,
  extra_usage_limit               REAL,
  extra_usage_used                REAL,
  extra_usage_pct                 REAL,
  extra_usage_currency            TEXT
);

CREATE TABLE IF NOT EXISTS usage_limits (
  ts            INTEGER NOT NULL,             -- = usage_samples.ts; pruned/preserved together, no FK (pragma off)
  kind          TEXT    NOT NULL,             -- session | weekly_all | weekly_scoped | ...
  lim_group     TEXT    NOT NULL DEFAULT '',  -- "group" is a reserved word
  percent       REAL    NOT NULL DEFAULT 0,
  severity      TEXT    NOT NULL DEFAULT '',  -- free text, unvalidated
  resets_at     TEXT,                         -- RFC3339Nano UTC, NULL when API sends null
  scope_model   TEXT    NOT NULL DEFAULT '',  -- scope.model.display_name; '' = unscoped
  scope_surface TEXT    NOT NULL DEFAULT '',  -- raw JSON of scope.surface; '' = null/absent
  is_active     INTEGER NOT NULL DEFAULT 0,
  -- Scope columns are NOT NULL '' (not nullable) deliberately: SQLite rowid
  -- tables allow NULLs in PK columns and treat each NULL as distinct, so a
  -- nullable-column PK would never dedupe.
  PRIMARY KEY (ts, kind, scope_model, scope_surface)
);
