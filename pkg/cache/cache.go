// Package cache persists parsed messages and time-bucketed token aggregates in a local SQLite database (modernc.org/sqlite).
package cache

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"modernc.org/sqlite"
)

// tsFormat is the canonical layout for persisting and parsing
// messages.ts. Every reader/writer path that touches the ts column MUST
// use this const — format drift would silently misroute rows through
// local_day's parse, dropping counts into a phantom "" day bucket.
const tsFormat = "2006-01-02T15:04:05.000Z07:00"

// init registers a deterministic local_day(ts) SQL scalar function. It
// projects a tsFormat-encoded UTC timestamp string into the calendar
// day of time.Local and returns "YYYY-MM-DD" — used by the 24h zoom
// path to bucket messages by local-tz day.
//
// Why not SQLite's built-in `strftime('%Y-%m-%d', ts, 'localtime')`?
// modernc.org/sqlite's transpiled libc reads tzdata differently per
// platform. On Darwin, libc_darwin.go:Xlocaltime calls
// getLocalLocation() which honors Go's time.Local. On Linux, the musl
// transpilation routes through X__secs_to_zone, which reads tzdata
// via libc's frozen os.Environ() snapshot — completely bypassing Go's
// time.Local. Tests that mutate time.Local therefore work on Mac and
// silently fall back to UTC on Linux. This Go-side function reads
// time.Local at call time, behaving identically on both platforms.
//
// Deterministic flag contract: time.Local is treated as a
// process-lifetime constant outside tests. Tests that mutate it (see
// withTimeLocal in cache_test.go) open a fresh Cache per test, so
// SQLite's per-statement function-call dedupe never spans a mutation.
// Any future in-process tz mutation outside tests would surface as
// stale bucket grouping.
//
// Errors are returned (not silently mapped to "") so a future format
// drift between local_day and the writer at InsertMessages fails the
// SELECT loudly rather than silently undercounting the chart.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(
		"local_day",
		1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ts, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("local_day: ts arg is %T, want string", args[0])
			}
			t, err := time.Parse(tsFormat, ts)
			if err != nil {
				return nil, fmt.Errorf("local_day: parse ts %q: %w", ts, err)
			}
			return t.In(time.Local).Format("2006-01-02"), nil
		},
	)
}

//go:embed schema.sql
var schemaSQL string

// SchemaVersion is the expected on-disk schema version; a mismatch triggers an auto-rebuild.
const SchemaVersion = "9"

// normalizeResetsAtSQL flips legacy `0001-01-01T00:00:00Z` sentinels
// (written before issue #189 landed) to SQL NULL across every
// *_resets_at column. Runs on every Open; idempotent — re-running on a
// clean DB matches zero rows. Cheap: usage_samples holds <2000 rows
// over ccpulse's useful history.
const normalizeResetsAtSQL = `
UPDATE usage_samples SET five_hour_resets_at            = NULL WHERE five_hour_resets_at            = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_resets_at            = NULL WHERE seven_day_resets_at            = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_sonnet_resets_at     = NULL WHERE seven_day_sonnet_resets_at     = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_opus_resets_at       = NULL WHERE seven_day_opus_resets_at       = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_omelette_resets_at   = NULL WHERE seven_day_omelette_resets_at   = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_oauth_apps_resets_at = NULL WHERE seven_day_oauth_apps_resets_at = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET seven_day_cowork_resets_at     = NULL WHERE seven_day_cowork_resets_at     = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET tangelo_resets_at              = NULL WHERE tangelo_resets_at              = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET iguana_necktie_resets_at       = NULL WHERE iguana_necktie_resets_at       = '0001-01-01T00:00:00Z';
UPDATE usage_samples SET omelette_promotional_resets_at = NULL WHERE omelette_promotional_resets_at = '0001-01-01T00:00:00Z';
`

// cachePragmas is appended to the DSN so modernc.org/sqlite applies them
// on every new pool connection, not just the first one. Issuing pragmas
// post-Open via db.Exec only configures whichever connection database/sql
// hands out; subsequent connections start with driver defaults, leaving
// readers and writers under the un-tuned busy_timeout=0 / synchronous=FULL.
// busy_timeout sorts first inside the driver so later pragmas can wait.
const cachePragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(wal)" +
	"&_pragma=synchronous(normal)" +
	"&_pragma=temp_store(memory)"

// Cache is the SQLite-backed store for message rows, file cursors, and usage samples.
type Cache struct {
	db *sql.DB
	// lockFile is the fd holding the cache flock. Must not be dup'd or passed
	// to a subprocess (would defeat OS-on-close release).
	lockFile *os.File
}

// errSchemaMismatch is the internal signal openDB returns when the
// on-disk schema_version differs from SchemaVersion. Callers that
// want auto-rebuild dispatch to LockedRebuild; callers that don't
// (LockedRebuild itself) treat it as a hard error since they just
// recreated the DB.
var errSchemaMismatch = errors.New("on-disk schema version mismatch")

// openDB opens the SQLite DB at path, applies the embedded schema,
// reads the schema_version row, and runs post-open normalization.
// Returns errSchemaMismatch if the on-disk version differs from
// SchemaVersion — caller decides how to handle it.
//
// openDB takes NO lock; the caller owns the flock policy.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		return nil, err
	}

	// Read the on-disk version BEFORE applying schemaSQL. A version mismatch
	// must dispatch to rebuild rather than apply the new schema over the old
	// tables: CREATE TABLE IF NOT EXISTS is a no-op on an existing table, so a
	// newly-added column never lands, and any new index referencing that column
	// (e.g. idx_messages_ts_repo_root in v8) would fail here with "no such
	// column" before the mismatch could be detected. A successful read of a
	// DIFFERENT version is the only "existing old DB" signal; a read error
	// (no meta table on a fresh/just-rebuilt file, or an empty meta row) falls
	// through to apply the idempotent schema.
	var version string
	if verr := db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); verr == nil &&
		version != SchemaVersion {
		db.Close()
		return nil, errSchemaMismatch
	}

	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version',?)`, SchemaVersion); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, normalizeResetsAtSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("normalize legacy resets_at sentinels: %w", err)
	}
	return db, nil
}

// Open opens the cache at path and acquires LOCK_SH on path+".lock".
// Returns ErrLockHeld if another process holds LOCK_EX (i.e. a
// rebuild is in progress). On schema-version mismatch, Open releases
// its SH lock and dispatches to LockedRebuild; the cross-fd race
// during binary upgrades surfaces as ErrLockHeld for the loser.
//
// Cache.Close releases the lock fd.
func Open(ctx context.Context, path string) (*Cache, error) {
	lockFile, err := acquireCacheLock(path+".lock", syscall.LOCK_SH)
	if err != nil {
		return nil, err
	}
	db, err := openDB(ctx, path)
	if errors.Is(err, errSchemaMismatch) {
		// Release SH so LockedRebuild can take EX on a fresh fd
		// without contention from this process. LockedRebuild
		// performs the unlink itself.
		lockFile.Close()
		return LockedRebuild(ctx, path)
	}
	if err != nil {
		lockFile.Close()
		return nil, err
	}
	return &Cache{db: db, lockFile: lockFile}, nil
}

// NewFromDB wraps an already-open *sql.DB in a *Cache without re-running
// the schema apply / version check that Open does. Used by callers that
// receive the raw *sql.DB (e.g. status.Compute) and need Cache methods
// without re-opening the file.
func NewFromDB(db *sql.DB) *Cache {
	return &Cache{db: db}
}

// DB returns the underlying *sql.DB for callers that need direct query access.
func (c *Cache) DB() *sql.DB { return c.db }

// Close closes the underlying DB and releases the cache lock fd.
// Both operations run unconditionally; the DB close error is
// returned in preference to the lock release error (DB close is
// the more interesting failure mode in practice).
//
// Close is not safe for concurrent use; the caller must serialize.
func (c *Cache) Close() error {
	var dbErr error
	if c.db != nil {
		dbErr = c.db.Close()
	}
	var lockErr error
	if c.lockFile != nil {
		// flock release is implicit on close — no separate LOCK_UN needed.
		lockErr = c.lockFile.Close()
		c.lockFile = nil
	}
	if dbErr != nil {
		if lockErr != nil {
			slog.Warn("cache.lockReleaseFailed", "err", lockErr)
		}
		return dbErr
	}
	return lockErr
}

// removeWithSiblings deletes path plus its SQLite sidecar files
// (-wal, -shm, -journal). path must be the SQLite DB file path (not a
// directory and without a trailing separator); the sidecar names are
// formed by simple suffix concatenation. Missing files are not an
// error; any other removal failure is wrapped and returned without
// attempting later siblings.
//
// Unexported: LockedRebuild is the ONLY legal caller. Direct unlinks
// from outside pkg/cache risk the silent-corruption vector from
// issue #219.
func removeWithSiblings(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s%s: %w", path, suffix, err)
		}
	}
	return nil
}

// RecordUsageSample inserts a row at when.UTC().Unix() with one column per
// anthro.Usage bucket, plus one usage_limits child row per entry of u.Limits,
// all in a single transaction. INSERT OR IGNORE: same-second collisions keep
// the first row — child inserts are gated on the parent insert landing, so
// parent and children can never mix samples. Nil buckets write NULL into both
// their pct and resets_at columns.
func (c *Cache) RecordUsageSample(ctx context.Context, u anthro.Usage, when time.Time) error {
	ts := when.UTC().Unix()
	args := []any{ts, "api"}
	args = append(args, bucketArgs(u.FiveHour)...)
	args = append(args, bucketArgs(u.SevenDay)...)
	args = append(args, bucketArgs(u.SevenDaySonnet)...)
	args = append(args, bucketArgs(u.SevenDayOpus)...)
	args = append(args, bucketArgs(u.SevenDayOmelette)...)
	args = append(args, bucketArgs(u.SevenDayOauthApps)...)
	args = append(args, bucketArgs(u.SevenDayCowork)...)
	args = append(args, bucketArgs(u.Tangelo)...)
	args = append(args, bucketArgs(u.IguanaNecktie)...)
	args = append(args, bucketArgs(u.OmelettePromotional)...)
	args = append(args, extraUsageArgs(u.ExtraUsage)...)

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("record usage sample: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, insertUsageSampleSQL, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		for _, l := range u.Limits {
			if _, err := tx.ExecContext(ctx, insertUsageLimitSQL, limitArgs(ts, l)...); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record usage sample: commit: %w", err)
	}
	return nil
}

const insertUsageSampleSQL = `INSERT OR IGNORE INTO usage_samples(
	ts, source,
	five_hour_pct, five_hour_resets_at,
	seven_day_pct, seven_day_resets_at,
	seven_day_sonnet_pct, seven_day_sonnet_resets_at,
	seven_day_opus_pct, seven_day_opus_resets_at,
	seven_day_omelette_pct, seven_day_omelette_resets_at,
	seven_day_oauth_apps_pct, seven_day_oauth_apps_resets_at,
	seven_day_cowork_pct, seven_day_cowork_resets_at,
	tangelo_pct, tangelo_resets_at,
	iguana_necktie_pct, iguana_necktie_resets_at,
	omelette_promotional_pct, omelette_promotional_resets_at,
	extra_usage_enabled, extra_usage_limit, extra_usage_used, extra_usage_pct, extra_usage_currency
) VALUES (
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?,
	?, ?, ?, ?, ?
)`

func bucketArgs(b *anthro.Bucket) []any {
	if b == nil {
		return []any{nil, nil}
	}
	if b.ResetsAt == nil {
		return []any{b.Utilization, nil}
	}
	return []any{b.Utilization, b.ResetsAt.UTC().Format(time.RFC3339Nano)}
}

func extraUsageArgs(e *anthro.ExtraUsage) []any {
	if e == nil {
		return []any{nil, nil, nil, nil, nil}
	}
	var enabled any = 0
	if e.IsEnabled {
		enabled = 1
	}
	var pct any
	if e.Utilization != nil {
		pct = *e.Utilization
	}
	return []any{enabled, e.MonthlyLimit, e.UsedCredits, pct, e.Currency}
}

const insertUsageLimitSQL = `INSERT OR IGNORE INTO usage_limits(
	ts, kind, lim_group, percent, severity, resets_at, scope_model, scope_surface, is_active
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

// limitArgs flattens one limits entry into insertUsageLimitSQL's argument
// list. Scope columns collapse to '' when absent so the composite primary
// key dedupes (SQLite treats NULLs in a PK as distinct). scope_surface
// stores the raw JSON of an unrecognized-shape surface; the observed
// literal null collapses to '' like an absent scope.
func limitArgs(ts int64, l anthro.Limit) []any {
	var resetsAt any
	if l.ResetsAt != nil {
		resetsAt = l.ResetsAt.UTC().Format(time.RFC3339Nano)
	}
	var scopeModel, scopeSurface string
	if l.Scope != nil {
		if l.Scope.Model != nil && l.Scope.Model.DisplayName != nil {
			scopeModel = *l.Scope.Model.DisplayName
		}
		if s := string(l.Scope.Surface); s != "" && s != "null" {
			scopeSurface = s
		}
	}
	active := 0
	if l.IsActive {
		active = 1
	}
	return []any{ts, l.Kind, l.Group, l.Percent, l.Severity, resetsAt, scopeModel, scopeSurface, active}
}

// PruneUsageSamples deletes rows with ts < cutoff.UTC().Unix().
// Returns the number of rows deleted.
func (c *Cache) PruneUsageSamples(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := c.db.ExecContext(ctx, `DELETE FROM usage_samples WHERE ts < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SevenDaySample is a single usage_samples row projected to the columns
// needed by status.projectSevenDay. ResetsAt is normalized to minute
// precision (RFC3339, UTC) so sub-second jitter in the API response doesn't
// fragment otherwise-equal bucket boundaries; consumers treat it as an
// opaque equality key for bucket-membership filtering.
type SevenDaySample struct {
	At       time.Time
	Pct      float64
	ResetsAt string
}

// SevenDaySamplesSince returns ordered (At, Pct, ResetsAt) tuples for
// usage_samples rows where ts >= since.UTC().Unix() and seven_day_pct
// IS NOT NULL, oldest-first. Used by status.Compute to derive the
// trailing-window slope for the 7d projection.
//
// On any SQL error the caller should fall back to the linear projection.
func (c *Cache) SevenDaySamplesSince(ctx context.Context, since time.Time) ([]SevenDaySample, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT ts, seven_day_pct, seven_day_resets_at
FROM usage_samples
WHERE ts >= ? AND seven_day_pct IS NOT NULL
ORDER BY ts ASC`, since.UTC().Unix())
	if err != nil {
		return nil, fmt.Errorf("query usage_samples: %w", err)
	}
	defer rows.Close()

	var out []SevenDaySample
	for rows.Next() {
		var (
			ts       int64
			pct      float64
			resetsAt sql.NullString
		)
		if err := rows.Scan(&ts, &pct, &resetsAt); err != nil {
			return nil, fmt.Errorf("scan usage_samples row: %w", err)
		}
		if !resetsAt.Valid {
			// Upstream Anthropic glitch: 7d bucket has a real pct but
			// resets_at came back null. Filter so projection slope math
			// doesn't ingest a bogus bucket boundary.
			warnOnceNullResets("seven_day_resets_at")
			continue
		}
		// Normalize ResetsAt to minute precision so jitter in the API response
		// doesn't create spurious distinct bucket IDs. Truncate(second) failed
		// when jitter straddled a whole-second boundary (08:59:59.9 vs
		// 09:00:00.1 truncate to distinct seconds); Round(minute) merges them,
		// and 7d resets are calendar-aligned so genuine resets never fall
		// <30s apart. See issue #395.
		normalizedResetsAt := resetsAt.String
		if t, err := time.Parse(time.RFC3339Nano, resetsAt.String); err == nil {
			normalizedResetsAt = t.UTC().Round(time.Minute).Format(time.RFC3339)
		}
		out = append(out, SevenDaySample{
			At:       time.Unix(ts, 0).UTC(),
			Pct:      pct,
			ResetsAt: normalizedResetsAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_samples rows: %w", err)
	}
	return out, nil
}

// UtilizationPoint is a single usage_samples row projected to a timestamp
// and a utilization percentage. Used by the TUI remaining-quota line chart.
type UtilizationPoint struct {
	At  time.Time
	Pct float64
}

// utilizationPolicy is the read-time policy for one (pct, resets_at)
// pair: which resets_at column to inspect, and whether NULL resets_at
// rows should be filtered out (true for calendar-aligned 7d buckets
// where NULL is an Anthropic-side glitch; false for the 5h rolling
// window where NULL means "idle" and pct=0 is truthful).
type utilizationPolicy struct {
	resetsAtCol     string
	filterNullReset bool
}

// utilizationColumns is the allowlist + per-column policy that
// UtilizationSince consults. Prevents SQL injection — the pct column
// name is interpolated into the query string (not parameterisable in
// SQLite), and the resets_at column name comes from the policy.
var utilizationColumns = map[string]utilizationPolicy{
	"five_hour_pct": {"five_hour_resets_at", false},
	"seven_day_pct": {"seven_day_resets_at", true},
}

// UtilizationSince returns timestamped utilization percentages from
// usage_samples for the given column, oldest-first. Rows where the
// column IS NULL are skipped. column must be in the allowlist.
//
// Per-column null-resets_at policy: 5h rows with NULL resets_at are
// kept (idle window — pct=0 is truthful); 7d rows with NULL resets_at
// are filtered + emit the once-per-process WARN (calendar-bucket
// glitch). See issue #189.
func (c *Cache) UtilizationSince(ctx context.Context, column string, since time.Time) ([]UtilizationPoint, error) {
	policy, ok := utilizationColumns[column]
	if !ok {
		return nil, fmt.Errorf("invalid utilization column: %q", column)
	}
	rows, err := c.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT ts, %s, %s FROM usage_samples WHERE ts >= ? AND %s IS NOT NULL ORDER BY ts ASC`,
			column, policy.resetsAtCol, column),
		since.UTC().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage_samples: %w", err)
	}
	defer rows.Close()

	var out []UtilizationPoint
	for rows.Next() {
		var ts int64
		var pct float64
		var resetsAt sql.NullString
		if err := rows.Scan(&ts, &pct, &resetsAt); err != nil {
			return nil, fmt.Errorf("scan usage_samples row: %w", err)
		}
		if policy.filterNullReset && !resetsAt.Valid {
			warnOnceNullResets(policy.resetsAtCol)
			continue
		}
		out = append(out, UtilizationPoint{
			At:  time.Unix(ts, 0).UTC(),
			Pct: pct,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage_samples rows: %w", err)
	}
	return out, nil
}

// sqlExecutor is the subset of *sql.DB / *sql.Tx used by the row-insert and
// file-cursor helpers, so each can run either standalone (auto-commit on
// *sql.DB) or inside a shared transaction (*sql.Tx).
type sqlExecutor interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// insertMessagesTx upserts msgs using q (a *sql.DB or an in-flight *sql.Tx).
// It performs no transaction control of its own; the caller owns begin/commit.
func insertMessagesTx(ctx context.Context, q sqlExecutor, msgs []parse.Message, hist pricing.History) error {
	stmt, err := q.PrepareContext(ctx, `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch, repo_root)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, message_id) DO UPDATE SET
  ts                    = min(excluded.ts, ts),
  input_tokens          = max(excluded.input_tokens, input_tokens),
  output_tokens         = max(excluded.output_tokens, output_tokens),
  cache_read_tokens     = max(excluded.cache_read_tokens, cache_read_tokens),
  cache_write_5m_tokens = max(excluded.cache_write_5m_tokens, cache_write_5m_tokens),
  cache_write_1h_tokens = max(excluded.cache_write_1h_tokens, cache_write_1h_tokens),
  cost_usd_estimate     = max(excluded.cost_usd_estimate, cost_usd_estimate)`)
	if err != nil {
		return fmt.Errorf("insert messages: prepare: %w", err)
	}
	defer stmt.Close()

	for _, m := range msgs {
		cost, version, unknown := hist.CostFor(m)
		unk := 0
		if unknown {
			unk = 1
		}
		sub := 0
		if m.IsSubagent {
			sub = 1
		}
		tsStr := m.Timestamp.UTC().Format(tsFormat)
		msgID := m.MessageID
		if msgID == "" {
			msgID = "synthetic:" + tsStr
		}
		if _, err := stmt.ExecContext(ctx,
			m.SessionID, msgID, m.ProjectSlug, tsStr,
			m.Role, m.Model,
			m.InputTokens, m.OutputTokens, m.CacheReadTokens,
			m.CacheWrite5mTokens, m.CacheWrite1hTokens,
			cost, version, unk, sub, m.ParentSessionID, m.Cwd, m.GitBranch, m.RepoRoot,
		); err != nil {
			return fmt.Errorf("insert messages: exec: %w", err)
		}
	}
	return nil
}

// recordFileTx upserts the byte-offset cursor for path using q (a *sql.DB or an
// in-flight *sql.Tx). No transaction control of its own.
func recordFileTx(ctx context.Context, q sqlExecutor, path string, mtimeNs, offset, lastLine int64) error {
	if _, err := q.ExecContext(ctx, `
INSERT INTO files(path, mtime_ns, last_offset_bytes, last_line)
VALUES (?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  mtime_ns = excluded.mtime_ns,
  last_offset_bytes = excluded.last_offset_bytes,
  last_line = excluded.last_line
`, path, mtimeNs, offset, lastLine); err != nil {
		return fmt.Errorf("record file: %w", err)
	}
	return nil
}

// InsertMessages upserts parsed messages into the messages table, collapsing
// the multiple content-block lines of one logical assistant turn into a single
// row keyed by (session_id, message_id). Lines without a message.id fall back
// to a synthetic "synthetic:<ts>" key, preserving the legacy (session_id, ts)
// identity. The "synthetic:" prefix is RESERVED: real message.id values must
// not begin with it. On conflict it keeps the MAX of every cumulative usage
// column (the per-line usage repeats the message total, so MAX == the final
// total and is robust to streaming partial lines) and the MIN of ts (turn start).
func (c *Cache) InsertMessages(ctx context.Context, msgs []parse.Message, hist pricing.History) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert messages: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := insertMessagesTx(ctx, tx, msgs, hist); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert messages: commit: %w", err)
	}
	return nil
}

// RecordFile upserts the byte-offset cursor and mtime for path into the files table.
func (c *Cache) RecordFile(ctx context.Context, path string, mtimeNs, offset, lastLine int64) error {
	return recordFileTx(ctx, c.db, path, mtimeNs, offset, lastLine)
}

// InsertMessagesAndRecordFile upserts msgs and advances the file cursor for
// path inside a single transaction: either both land or neither does. This
// closes the window where a crash between two separate commits would persist
// rows without advancing the cursor (forcing a redundant re-parse next run),
// and the synthetic-key edge where two content-block lines sharing a timestamp
// collapse on re-parse. msgs may be empty, in which case only the cursor is
// advanced — still transactionally.
func (c *Cache) InsertMessagesAndRecordFile(
	ctx context.Context,
	msgs []parse.Message, hist pricing.History,
	path string, mtimeNs, offset, lastLine int64,
) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ingest file: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if len(msgs) > 0 {
		if err := insertMessagesTx(ctx, tx, msgs, hist); err != nil {
			return err
		}
	}
	if err := recordFileTx(ctx, tx, path, mtimeNs, offset, lastLine); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ingest file: commit: %w", err)
	}
	return nil
}

// GetFile looks up the stored byte-offset cursor for path; found is false when the file has not been indexed yet.
func (c *Cache) GetFile(ctx context.Context, path string) (mtime, offset, line int64, found bool, err error) {
	row := c.db.QueryRowContext(ctx, `SELECT mtime_ns, last_offset_bytes, last_line FROM files WHERE path = ?`, path)
	err = row.Scan(&mtime, &offset, &line)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	return mtime, offset, line, true, nil
}

// AllFileOffsets returns path → last_offset_bytes for every recorded
// file in one query. Lets enumerators decide which files need
// processing without O(N) GetFile round-trips. Returns an empty
// (non-nil) map when the table is empty. A missing key is the
// caller's signal that the file is unrecorded — semantically
// equivalent to GetFile's found=false path.
func (c *Cache) AllFileOffsets(ctx context.Context) (map[string]int64, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT path, last_offset_bytes FROM files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var path string
		var offset int64
		if err := rows.Scan(&path, &offset); err != nil {
			return nil, err
		}
		out[path] = offset
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// IntegrityOK runs `PRAGMA integrity_check` and reports whether SQLite
// considers the database file healthy. Returns (false, nil) if SQLite
// reports a non-"ok" result. Returns a non-nil error on ctx cancellation
// or driver failure — callers should distinguish corrupt-DB (err == nil,
// ok == false) from cancellation (errors.Is(err, context.Canceled)).
func (c *Cache) IntegrityOK(ctx context.Context) (bool, error) {
	row := c.db.QueryRowContext(ctx, `PRAGMA integrity_check`)
	var s string
	if err := row.Scan(&s); err != nil {
		return false, err
	}
	return s == "ok", nil
}

// BucketAlign snaps t down to the nearest multiple of dur in unix
// seconds and returns the result in UTC. It is the canonical helper for
// computing bucket-boundary times shared between cache and TUI; the
// cache also re-applies it defensively on IOTokenBuckets bounds.
func BucketAlign(t time.Time, dur time.Duration) time.Time {
	s := int64(dur.Seconds())
	return time.Unix((t.Unix()/s)*s, 0).UTC()
}

// DayStartLocal returns local midnight of t's calendar day in time.Local.
// The returned value's Location() is time.Local — callers should treat it
// as a local-tz value, not UTC. Used by the 24h zoom path for from/to
// computation; sub-day zooms use the UTC-aligned BucketAlign above.
func DayStartLocal(t time.Time) time.Time {
	y, mo, d := t.In(time.Local).Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, time.Local)
}

// TokenBucket is one time-bucketed total of token usage.
//
// For sub-day durations (15m/1h), BucketStart is in UTC. For the 24h
// zoom, BucketStart is in time.Local (parsed via dayStartLocal). Callers
// rendering the value should not assume Location() == time.UTC.
type TokenBucket struct {
	BucketStart time.Time
	Tokens      int64
}

// IOTokenBuckets returns one TokenBucket per `dur` interval covering
// [from, to). Both bounds are snapped down to bucket boundaries before
// query (BucketAlign is idempotent, so callers may also pre-snap).
// Empty intervals are returned with Tokens == 0; output is ordered
// oldest-first, len = (to.Sub(from) / dur).
//
// The Tokens field is SUM(input_tokens + output_tokens) per bucket —
// matches Claude Code `/usage` Tokens-per-Day chart semantics. Cache
// reads and writes are deliberately excluded; use CostBuckets for the
// rate-weighted blend across all five columns. See issue #232 (revises
// the output-only choice made in #209).
func (c *Cache) IOTokenBuckets(ctx context.Context, dur time.Duration, from, to time.Time) ([]TokenBucket, error) {
	if dur == 24*time.Hour {
		return c.ioTokenBucketsDaily(ctx, from, to)
	}
	start := time.Now()
	from = BucketAlign(from, dur)
	to = BucketAlign(to, dur)
	if !to.After(from) {
		return nil, nil
	}
	bucketSecs := int64(dur.Seconds())
	fromStr := from.UTC().Format(tsFormat)
	toStr := to.UTC().Format(tsFormat)
	rows, err := c.db.QueryContext(ctx, `
SELECT
  CAST(CAST(strftime('%s', ts) AS INTEGER) / ? AS INTEGER) * ? AS bucket_epoch,
  COALESCE(SUM(input_tokens + output_tokens), 0)
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY bucket_epoch
ORDER BY bucket_epoch ASC
`, bucketSecs, bucketSecs, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := make(map[int64]int64)
	for rows.Next() {
		var epoch, tokens int64
		if err := rows.Scan(&epoch, &tokens); err != nil {
			return nil, err
		}
		totals[epoch] = tokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	n := int(to.Sub(from) / dur)
	out := make([]TokenBucket, n)
	for i := range n {
		bs := from.Add(time.Duration(i) * dur)
		out[i] = TokenBucket{
			BucketStart: bs,
			Tokens:      totals[bs.Unix()],
		}
	}
	slog.Debug("cache.IOTokenBuckets",
		"dur_ms", time.Since(start).Milliseconds(),
		"zoom", zoomLabel(dur),
		"buckets", n,
		"rows_aggregated", len(totals))
	return out, nil
}

// dailyBuckets aggregates messages into one bucket per local-tz calendar
// day in [from, to). aggExpr is a trusted compile-time SQL fragment — the
// SUM(...) expression — and is NEVER user input (the two call sites pass
// string literals), so the fmt.Sprintf into the query is safe. build
// constructs a typed bucket from a local-midnight day start and the
// aggregated value.
//
// The local_day(ts) call uses the Go-registered scalar function (see init()
// above) so day grouping honours time.Local on every platform. Iteration
// uses AddDate(0, 0, 1): it advances by one calendar day in time.Local,
// producing a single bucket per DST transition day (23h spring-forward or
// 25h fall-back). Add(24*time.Hour) would drift by one hour across each
// transition and mis-bucket messages near local midnight.
//
// BucketStart values are in time.Local — callers must not assume UTC.
func dailyBuckets[V int64 | float64, T any](
	ctx context.Context,
	c *Cache, unit, aggExpr string,
	from, to time.Time,
	build func(start time.Time, v V) T,
) ([]T, error) {
	start := time.Now()
	from = DayStartLocal(from)
	to = DayStartLocal(to)
	if !to.After(from) {
		return nil, nil
	}
	fromStr := from.UTC().Format(tsFormat)
	toStr := to.UTC().Format(tsFormat)
	//nolint:gosec // G201: aggExpr is a controlled enum from internal call sites, not user input
	query := fmt.Sprintf(`
SELECT
  local_day(ts) AS day,
  %s
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY day
ORDER BY day ASC
`, aggExpr)
	rows, err := c.db.QueryContext(ctx, query, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := make(map[string]V)
	for rows.Next() {
		var day string
		var v V
		if err := rows.Scan(&day, &v); err != nil {
			return nil, err
		}
		totals[day] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]T, 0, int(to.Sub(from)/(24*time.Hour))+1)
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		out = append(out, build(d, totals[key]))
	}
	slog.Debug("cache.dailyBuckets",
		"unit", unit,
		"zoom", "24h",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(out),
		"rows_aggregated", len(totals))
	return out, nil
}

// ioTokenBucketsDaily returns one TokenBucket per local-tz calendar day in
// [from, to), aggregating SUM(input_tokens + output_tokens). The COALESCE is
// defensive only (both columns are NOT NULL). See dailyBuckets for the
// local-tz / DST / iteration rationale.
func (c *Cache) ioTokenBucketsDaily(ctx context.Context, from, to time.Time) ([]TokenBucket, error) {
	return dailyBuckets(ctx, c, "io",
		"COALESCE(SUM(input_tokens + output_tokens), 0)", from, to,
		func(d time.Time, v int64) TokenBucket {
			return TokenBucket{BucketStart: d, Tokens: v}
		})
}

// CostBucket is one time-bucketed total of USD cost.
//
// For sub-day durations (15m/1h), BucketStart is in UTC. For the 24h
// zoom, BucketStart is in time.Local (parsed via dayStartLocal). Callers
// rendering the value should not assume Location() == time.UTC.
type CostBucket struct {
	BucketStart time.Time
	Cost        float64
}

// CostBuckets returns one CostBucket per `dur` interval covering
// [from, to). Mirrors IOTokenBuckets exactly except the aggregator is
// SUM(cost_usd_estimate). Messages with pricing_unknown=1 contribute 0
// to their bucket because cost_usd_estimate was stored as 0 at ingest;
// no extra WHERE clause is needed.
func (c *Cache) CostBuckets(ctx context.Context, dur time.Duration, from, to time.Time) ([]CostBucket, error) {
	if dur == 24*time.Hour {
		return c.costBucketsDaily(ctx, from, to)
	}
	start := time.Now()
	from = BucketAlign(from, dur)
	to = BucketAlign(to, dur)
	if !to.After(from) {
		return nil, nil
	}
	bucketSecs := int64(dur.Seconds())
	fromStr := from.UTC().Format(tsFormat)
	toStr := to.UTC().Format(tsFormat)
	rows, err := c.db.QueryContext(ctx, `
SELECT
  CAST(CAST(strftime('%s', ts) AS INTEGER) / ? AS INTEGER) * ? AS bucket_epoch,
  SUM(cost_usd_estimate)
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY bucket_epoch
ORDER BY bucket_epoch ASC
`, bucketSecs, bucketSecs, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := make(map[int64]float64)
	for rows.Next() {
		var epoch int64
		var cost float64
		if err := rows.Scan(&epoch, &cost); err != nil {
			return nil, err
		}
		totals[epoch] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	n := int(to.Sub(from) / dur)
	out := make([]CostBucket, n)
	for i := range n {
		bs := from.Add(time.Duration(i) * dur)
		out[i] = CostBucket{
			BucketStart: bs,
			Cost:        totals[bs.Unix()],
		}
	}
	slog.Debug("cache.CostBuckets",
		"dur_ms", time.Since(start).Milliseconds(),
		"zoom", zoomLabel(dur),
		"unit", "cost",
		"buckets", n,
		"rows_aggregated", len(totals))
	return out, nil
}

// costBucketsDaily returns one CostBucket per local-tz calendar day in
// [from, to), aggregating SUM(cost_usd_estimate). See dailyBuckets for the
// local-tz / DST / iteration rationale.
func (c *Cache) costBucketsDaily(ctx context.Context, from, to time.Time) ([]CostBucket, error) {
	return dailyBuckets(ctx, c, "cost",
		"SUM(cost_usd_estimate)", from, to,
		func(d time.Time, v float64) CostBucket {
			return CostBucket{BucketStart: d, Cost: v}
		})
}

// zoomLabel returns the compact human label that matches pkg/tui's
// ZoomLevels labels ("15m", "1h", "24h") for the three known zoom
// durations. Falls back to time.Duration.String() (e.g. "5m0s") for
// any other value. Keeps the slog "zoom" field consistent across
// pkg/cache and pkg/tui so a single grep correlates all four perf
// timing sites.
func zoomLabel(d time.Duration) string {
	switch d {
	case 15 * time.Minute:
		return "15m"
	case time.Hour:
		return "1h"
	case 24 * time.Hour:
		return "24h"
	}
	return d.String()
}

// EarliestMessageTime returns the timestamp of the oldest row in
// messages. ok == false when the table is empty (a routine first-launch
// state, not an error). On non-empty caches the returned time is in UTC.
func (c *Cache) EarliestMessageTime(ctx context.Context) (time.Time, bool, error) {
	var s sql.NullString
	if err := c.db.QueryRowContext(ctx, `SELECT MIN(ts) FROM messages`).Scan(&s); err != nil {
		return time.Time{}, false, err
	}
	if !s.Valid {
		return time.Time{}, false, nil
	}
	t, err := time.Parse(tsFormat, s.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse earliest ts %q: %w", s.String, err)
	}
	return t.UTC(), true, nil
}

// nullResetsWarned tracks which column names have already produced a
// once-per-process WARN from warnOnceNullResets. Keyed by column name
// string → *atomic.Bool; LoadOrStore + Swap give a lock-free
// "log the first call per key" gate.
var nullResetsWarned sync.Map

// warnOnceNullResets emits a single WARN line per (column, process
// lifetime) when the read layer filters a usage_samples row whose
// *_resets_at value is NULL. Used by the 7d-glitch filter in
// SevenDaySamplesSince and the per-column policy in UtilizationSince
// (see issue #189). At most three keys are expected today
// (seven_day_resets_at, seven_day_sonnet_resets_at,
// seven_day_opus_resets_at); 5h null-resets rows are kept (idle window
// is legitimate) and don't trigger this.
func warnOnceNullResets(column string) {
	flag, _ := nullResetsWarned.LoadOrStore(column, &atomic.Bool{})
	b, ok := flag.(*atomic.Bool)
	if ok && !b.Swap(true) {
		slog.Warn("cache.UtilizationSince: filtered row with null resets_at",
			"column", column,
			"advisory", "treating as upstream glitch; see issue #189")
	}
}

// resetNullResetsWarnedForTest clears the once-per-column flag map.
// Test-only: production code never calls this. Exported via lowercase
// name within the package so tests in the same package can reset state
// between cases.
func resetNullResetsWarnedForTest() {
	nullResetsWarned.Range(func(k, _ any) bool {
		nullResetsWarned.Delete(k)
		return true
	})
}
