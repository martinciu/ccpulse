package cache

import (
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
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

const SchemaVersion = "6"

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

type Cache struct {
	db *sql.DB
}

func Open(path string) (*Cache, error) {
	db, err := sql.Open("sqlite", path+"?"+cachePragmas)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	var version string
	err = db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		db.Close()
		return nil, err
	}
	if err == nil && version != SchemaVersion {
		db.Close()
		if rmErr := RemoveWithSiblings(path); rmErr != nil {
			return nil, fmt.Errorf("wipe stale schema: %w", rmErr)
		}
		return Open(path)
	}

	if _, err := db.Exec(`INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version',?)`, SchemaVersion); err != nil {
		db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}

// NewFromDB wraps an already-open *sql.DB in a *Cache without re-running
// the schema apply / version check that Open does. Used by callers that
// receive the raw *sql.DB (e.g. status.Compute) and need Cache methods
// without re-opening the file.
func NewFromDB(db *sql.DB) *Cache {
	return &Cache{db: db}
}

func (c *Cache) DB() *sql.DB { return c.db }

func (c *Cache) Close() error { return c.db.Close() }

// RemoveWithSiblings deletes path plus its SQLite sidecar files
// (-wal, -shm, -journal). path must be the SQLite DB file path (not a
// directory and without a trailing separator); the sidecar names are
// formed by simple suffix concatenation. Missing files are not an
// error; any other removal failure is wrapped and returned without
// attempting later siblings. Use this — not raw os.Remove — at every
// state.db rebuild site, so a leftover -wal from a prior schema cannot
// be replayed onto a freshly-rebuilt main file.
func RemoveWithSiblings(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s%s: %w", path, suffix, err)
		}
	}
	return nil
}

// RecordUsageSample inserts a row at when.UTC().Unix() with one column per
// anthro.Usage bucket. INSERT OR IGNORE: same-second collisions keep the
// first row. Nil buckets write NULL into both their pct and resets_at columns.
func (c *Cache) RecordUsageSample(u anthro.Usage, when time.Time) error {
	args := []any{when.UTC().Unix(), "api"}
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

	_, err := c.db.Exec(insertUsageSampleSQL, args...)
	return err
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

// PruneUsageSamples deletes rows with ts < cutoff.UTC().Unix().
// Returns the number of rows deleted.
func (c *Cache) PruneUsageSamples(cutoff time.Time) (int64, error) {
	res, err := c.db.Exec(`DELETE FROM usage_samples WHERE ts < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SevenDaySample is a single usage_samples row projected to the columns
// needed by status.projectSevenDay. ResetsAt is normalized to second
// precision (RFC3339, UTC) so sub-second jitter in the API response
// doesn't fragment otherwise-equal bucket boundaries; consumers treat
// it as an opaque equality key for bucket-membership filtering.
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
func (c *Cache) SevenDaySamplesSince(since time.Time) ([]SevenDaySample, error) {
	rows, err := c.db.Query(`
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
		// Normalize ResetsAt to second precision so sub-second jitter in the
		// API response (nanoseconds differ across calls for the same logical
		// reset boundary) doesn't create spurious distinct bucket IDs.
		normalizedResetsAt := resetsAt.String
		if resetsAt.Valid {
			if t, err := time.Parse(time.RFC3339Nano, resetsAt.String); err == nil {
				normalizedResetsAt = t.UTC().Truncate(time.Second).Format(time.RFC3339)
			}
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

// utilizationColumns is the allowlist of columns that UtilizationSince
// accepts. Prevents SQL injection — the column name is interpolated
// into the query string (not parameterisable in SQLite).
var utilizationColumns = map[string]bool{
	"five_hour_pct": true,
	"seven_day_pct": true,
}

// UtilizationSince returns timestamped utilization percentages from
// usage_samples for the given column, oldest-first. Rows where the
// column IS NULL are skipped. column must be in the allowlist.
func (c *Cache) UtilizationSince(column string, since time.Time) ([]UtilizationPoint, error) {
	if !utilizationColumns[column] {
		return nil, fmt.Errorf("invalid utilization column: %q", column)
	}
	rows, err := c.db.Query(
		fmt.Sprintf(`SELECT ts, %s FROM usage_samples WHERE ts >= ? AND %s IS NOT NULL ORDER BY ts ASC`, column, column),
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
		if err := rows.Scan(&ts, &pct); err != nil {
			return nil, fmt.Errorf("scan usage_samples row: %w", err)
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

func (c *Cache) InsertMessages(msgs []parse.Message, hist pricing.History) error {
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("insert messages: begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
INSERT OR IGNORE INTO messages
(session_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
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
		if _, err := stmt.Exec(
			m.SessionID, m.ProjectSlug, m.Timestamp.UTC().Format(tsFormat),
			m.Role, m.Model,
			m.InputTokens, m.OutputTokens, m.CacheReadTokens,
			m.CacheWrite5mTokens, m.CacheWrite1hTokens,
			cost, version, unk, sub, m.ParentSessionID, m.Cwd, m.GitBranch,
		); err != nil {
			return fmt.Errorf("insert messages: exec: %w", err)
		}
	}
	return tx.Commit()
}

func (c *Cache) RecordFile(path string, mtimeNs, offset, lastLine int64) error {
	_, err := c.db.Exec(`
INSERT INTO files(path, mtime_ns, last_offset_bytes, last_line)
VALUES (?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
  mtime_ns = excluded.mtime_ns,
  last_offset_bytes = excluded.last_offset_bytes,
  last_line = excluded.last_line
`, path, mtimeNs, offset, lastLine)
	return err
}

func (c *Cache) GetFile(path string) (mtime, offset, line int64, found bool, err error) {
	row := c.db.QueryRow(`SELECT mtime_ns, last_offset_bytes, last_line FROM files WHERE path = ?`, path)
	err = row.Scan(&mtime, &offset, &line)
	if err == sql.ErrNoRows {
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
func (c *Cache) AllFileOffsets() (map[string]int64, error) {
	rows, err := c.db.Query(`SELECT path, last_offset_bytes FROM files`)
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
// considers the database file healthy. Returns false on any error or
// non-"ok" result.
func (c *Cache) IntegrityOK() bool {
	row := c.db.QueryRow(`PRAGMA integrity_check`)
	var s string
	if err := row.Scan(&s); err != nil {
		return false
	}
	return s == "ok"
}

// BucketAlign snaps t down to the nearest multiple of dur in unix
// seconds and returns the result in UTC. It is the canonical helper for
// computing bucket-boundary times shared between cache and TUI; the
// cache also re-applies it defensively on TokenBuckets bounds.
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

// TokenBuckets returns one TokenBucket per `dur` interval covering
// [from, to). Both bounds are snapped down to bucket boundaries before
// query (BucketAlign is idempotent, so callers may also pre-snap).
// Empty intervals are returned with Tokens == 0; output is ordered
// oldest-first, len = (to.Sub(from) / dur).
func (c *Cache) TokenBuckets(dur time.Duration, from, to time.Time) ([]TokenBucket, error) {
	if dur == 24*time.Hour {
		return c.tokenBucketsDaily(from, to)
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
	rows, err := c.db.Query(`
SELECT
  CAST(CAST(strftime('%s', ts) AS INTEGER) / ? AS INTEGER) * ? AS bucket_epoch,
  SUM(input_tokens + output_tokens + cache_read_tokens
      + cache_write_5m_tokens + cache_write_1h_tokens)
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
	slog.Debug("cache.TokenBuckets",
		"dur_ms", time.Since(start).Milliseconds(),
		"zoom", zoomLabel(dur),
		"buckets", n,
		"rows_aggregated", len(totals))
	return out, nil
}

// dailySQL is the shared 24h-zoom SQL form used by both tokenBucketsDaily
// and costBucketsDaily, parameterised by the aggregate expression. The
// local_day(ts) call uses the Go-registered scalar function (see init()
// above) so day grouping honours time.Local on every platform.
//
// tokenBucketsDaily returns one TokenBucket per local-tz calendar day in
// [from, to). Bucket boundaries are local midnight; SQLite groups via
// the registered local_day(ts) function. Iteration uses AddDate(0, 0, 1)
// so DST transitions (spring-forward 23h day, fall-back 25h day) each
// produce exactly one bucket.
//
// BucketStart values are in time.Local — callers must not assume UTC.
func (c *Cache) tokenBucketsDaily(from, to time.Time) ([]TokenBucket, error) {
	start := time.Now()
	from = DayStartLocal(from)
	to = DayStartLocal(to)
	if !to.After(from) {
		return nil, nil
	}
	fromStr := from.UTC().Format(tsFormat)
	toStr := to.UTC().Format(tsFormat)
	rows, err := c.db.Query(`
SELECT
  local_day(ts) AS day,
  SUM(input_tokens + output_tokens + cache_read_tokens
      + cache_write_5m_tokens + cache_write_1h_tokens)
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY day
ORDER BY day ASC
`, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := make(map[string]int64)
	for rows.Next() {
		var day string
		var tokens int64
		if err := rows.Scan(&day, &tokens); err != nil {
			return nil, err
		}
		totals[day] = tokens
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// AddDate(0, 0, 1) is load-bearing: it advances by one calendar day in
	// time.Local, producing a single bucket per DST transition day (23h or
	// 25h). Add(24*time.Hour) would drift by one hour across each
	// transition and mis-bucket messages near local midnight.
	out := make([]TokenBucket, 0, int(to.Sub(from)/(24*time.Hour))+1)
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		out = append(out, TokenBucket{BucketStart: d, Tokens: totals[key]})
	}
	slog.Debug("cache.tokenBucketsDaily",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(out),
		"rows_aggregated", len(totals))
	return out, nil
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
// [from, to). Mirrors TokenBuckets exactly except the aggregator is
// SUM(cost_usd_estimate). Messages with pricing_unknown=1 contribute 0
// to their bucket because cost_usd_estimate was stored as 0 at ingest;
// no extra WHERE clause is needed.
func (c *Cache) CostBuckets(dur time.Duration, from, to time.Time) ([]CostBucket, error) {
	if dur == 24*time.Hour {
		return c.costBucketsDaily(from, to)
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
	rows, err := c.db.Query(`
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

// costBucketsDaily mirrors tokenBucketsDaily for SUM(cost_usd_estimate).
// See tokenBucketsDaily docs for the local-tz / DST / iteration rationale.
func (c *Cache) costBucketsDaily(from, to time.Time) ([]CostBucket, error) {
	start := time.Now()
	from = DayStartLocal(from)
	to = DayStartLocal(to)
	if !to.After(from) {
		return nil, nil
	}
	fromStr := from.UTC().Format(tsFormat)
	toStr := to.UTC().Format(tsFormat)
	rows, err := c.db.Query(`
SELECT
  local_day(ts) AS day,
  SUM(cost_usd_estimate)
FROM messages
WHERE ts >= ? AND ts < ?
GROUP BY day
ORDER BY day ASC
`, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	totals := make(map[string]float64)
	for rows.Next() {
		var day string
		var cost float64
		if err := rows.Scan(&day, &cost); err != nil {
			return nil, err
		}
		totals[day] = cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// See tokenBucketsDaily for the AddDate(0,0,1) DST-correctness rationale.
	out := make([]CostBucket, 0, int(to.Sub(from)/(24*time.Hour))+1)
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		out = append(out, CostBucket{BucketStart: d, Cost: totals[key]})
	}
	slog.Debug("cache.costBucketsDaily",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(out),
		"rows_aggregated", len(totals))
	return out, nil
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
func (c *Cache) EarliestMessageTime() (time.Time, bool, error) {
	var s sql.NullString
	if err := c.db.QueryRow(`SELECT MIN(ts) FROM messages`).Scan(&s); err != nil {
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
