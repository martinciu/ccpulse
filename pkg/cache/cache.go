package cache

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const SchemaVersion = "5"

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

func (c *Cache) InsertMessages(msgs []parse.Message, tab pricing.Table) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
INSERT OR IGNORE INTO messages
(session_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, m := range msgs {
		cost, unknown := tab.CostFor(m)
		unk := 0
		if unknown {
			unk = 1
		}
		sub := 0
		if m.IsSubagent {
			sub = 1
		}
		if _, err := stmt.Exec(
			m.SessionID, m.ProjectSlug, m.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			m.Role, m.Model,
			m.InputTokens, m.OutputTokens, m.CacheReadTokens,
			m.CacheWrite5mTokens, m.CacheWrite1hTokens,
			cost, tab.Version, unk, sub, m.ParentSessionID,
		); err != nil {
			return err
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

type SlugCanonical struct {
	Slug          string
	CanonicalPath string
	Branch        string
	Resolved      bool
}

func (c *Cache) PutSlugCanonical(s SlugCanonical) error {
	r := 0
	if s.Resolved {
		r = 1
	}
	_, err := c.db.Exec(`
INSERT INTO slug_canonical(slug, canonical_path, worktree_branch, resolved, resolved_at)
VALUES (?,?,?,?,datetime('now'))
ON CONFLICT(slug) DO UPDATE SET
 canonical_path = excluded.canonical_path,
 worktree_branch = excluded.worktree_branch,
 resolved = excluded.resolved,
 resolved_at = excluded.resolved_at
`, s.Slug, s.CanonicalPath, s.Branch, r)
	return err
}

func (c *Cache) GetSlugCanonical(slug string) (SlugCanonical, bool, error) {
	row := c.db.QueryRow(`
SELECT slug, canonical_path, COALESCE(worktree_branch,''), resolved
FROM slug_canonical WHERE slug = ?`, slug)
	var s SlugCanonical
	var r int
	err := row.Scan(&s.Slug, &s.CanonicalPath, &s.Branch, &r)
	if err == sql.ErrNoRows {
		return SlugCanonical{}, false, nil
	}
	if err != nil {
		return SlugCanonical{}, false, err
	}
	s.Resolved = r != 0
	return s, true, nil
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

// TokenBucket is one time-bucketed total of token usage.
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
	start := time.Now()
	from = BucketAlign(from, dur)
	to = BucketAlign(to, dur)
	if !to.After(from) {
		return nil, nil
	}
	bucketSecs := int64(dur.Seconds())
	fromStr := from.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	toStr := to.UTC().Format("2006-01-02T15:04:05.000Z07:00")
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

// CostBucket is one time-bucketed total of USD cost.
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
	start := time.Now()
	from = BucketAlign(from, dur)
	to = BucketAlign(to, dur)
	if !to.After(from) {
		return nil, nil
	}
	bucketSecs := int64(dur.Seconds())
	fromStr := from.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	toStr := to.UTC().Format("2006-01-02T15:04:05.000Z07:00")
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

// zoomLabel returns the compact human label that matches pkg/tui's
// ZoomLevels labels ("5m", "15m", "1h") for the three known zoom
// durations. Falls back to time.Duration.String() (e.g. "5m0s") for
// any other value. Keeps the slog "zoom" field consistent across
// pkg/cache and pkg/tui so a single grep correlates all four perf
// timing sites.
func zoomLabel(d time.Duration) string {
	switch d {
	case 5 * time.Minute:
		return "5m"
	case 15 * time.Minute:
		return "15m"
	case time.Hour:
		return "1h"
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
	t, err := time.Parse("2006-01-02T15:04:05.000Z07:00", s.String)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse earliest ts %q: %w", s.String, err)
	}
	return t.UTC(), true, nil
}
