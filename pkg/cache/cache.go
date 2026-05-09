package cache

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const SchemaVersion = "4"

type Cache struct {
	db *sql.DB
}

func Open(path string) (*Cache, error) {
	db, err := sql.Open("sqlite", path)
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
		if rmErr := os.Remove(path); rmErr != nil {
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
 cost_usd_estimate, pricing_unknown,
 is_subagent, parent_session_id)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
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
			m.SessionID, m.ProjectSlug, m.Timestamp.Format("2006-01-02T15:04:05.000Z07:00"),
			m.Role, m.Model,
			m.InputTokens, m.OutputTokens, m.CacheReadTokens,
			m.CacheWrite5mTokens, m.CacheWrite1hTokens,
			cost, unk, sub, m.ParentSessionID,
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

type LiveSession struct {
	SessionID        string
	ProjectCanonical string
	WorktreeBranch   string
	Model            string
	LastTS           time.Time
	WorkingTimeSec   int64
	CostUSD          float64
}

// LiveSessions returns sessions with activity in [now-since, now],
// most-recent first.
type ModelTotals struct {
	Model      string
	Messages   int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64 // sum of 5m + 1h
	CostUSD    float64
}

func (c *Cache) TodayByModel(now time.Time) ([]ModelTotals, error) {
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).
		UTC().Format("2006-01-02T15:04:05.000Z07:00")
	rows, err := c.db.Query(`
SELECT model, COUNT(*), SUM(input_tokens), SUM(output_tokens),
       SUM(cache_read_tokens),
       SUM(cache_write_5m_tokens) + SUM(cache_write_1h_tokens),
       SUM(cost_usd_estimate)
FROM messages WHERE ts >= ?
GROUP BY model ORDER BY SUM(cost_usd_estimate) DESC
`, startOfDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelTotals
	for rows.Next() {
		var m ModelTotals
		if err := rows.Scan(&m.Model, &m.Messages, &m.Input, &m.Output,
			&m.CacheRead, &m.CacheWrite, &m.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type DayTotals struct {
	Date     string
	Sessions int64
	Tokens   int64
	CostUSD  float64
}

func (c *Cache) HistoryByDay(days int) ([]DayTotals, error) {
	rows, err := c.db.Query(`
SELECT substr(ts,1,10) AS d,
       COUNT(DISTINCT session_id),
       SUM(input_tokens+output_tokens+cache_read_tokens
           +cache_write_5m_tokens+cache_write_1h_tokens),
       SUM(cost_usd_estimate)
FROM messages
WHERE ts >= date('now', ?)
GROUP BY d ORDER BY d DESC
`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DayTotals
	for rows.Next() {
		var d DayTotals
		if err := rows.Scan(&d.Date, &d.Sessions, &d.Tokens, &d.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

type ProjectTotals struct {
	ProjectCanonical string
	Sessions         int64
	CostUSD          float64
	LastActive       time.Time
}

func (c *Cache) ProjectsTotals(days int) ([]ProjectTotals, error) {
	rows, err := c.db.Query(`
SELECT
  COALESCE(NULLIF(project_canonical, ''), project_slug) AS proj,
  COUNT(DISTINCT session_id),
  SUM(cost_usd_estimate),
  MAX(ts)
FROM messages
WHERE ts >= date('now', ?)
GROUP BY proj
ORDER BY SUM(cost_usd_estimate) DESC
`, fmt.Sprintf("-%d days", days))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProjectTotals
	for rows.Next() {
		var p ProjectTotals
		var last string
		if err := rows.Scan(&p.ProjectCanonical, &p.Sessions, &p.CostUSD, &last); err != nil {
			return nil, err
		}
		p.LastActive, _ = time.Parse("2006-01-02T15:04:05.000Z07:00", last)
		out = append(out, p)
	}
	return out, rows.Err()
}

type ModelsWindow string

const (
	WindowToday ModelsWindow = "today"
	Window7d    ModelsWindow = "7d"
	Window30d   ModelsWindow = "30d"
	WindowAll   ModelsWindow = "all"
)

func (c *Cache) ModelsTotals(w ModelsWindow) ([]ModelTotals, error) {
	var where string
	switch w {
	case WindowToday:
		where = "ts >= date('now')"
	case Window7d:
		where = "ts >= date('now','-7 days')"
	case Window30d:
		where = "ts >= date('now','-30 days')"
	default:
		where = "1=1"
	}
	q := fmt.Sprintf(`
SELECT model, COUNT(*), SUM(input_tokens), SUM(output_tokens),
       SUM(cache_read_tokens),
       SUM(cache_write_5m_tokens) + SUM(cache_write_1h_tokens),
       SUM(cost_usd_estimate)
FROM messages WHERE %s
GROUP BY model ORDER BY SUM(cost_usd_estimate) DESC
`, where)
	rows, err := c.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelTotals
	for rows.Next() {
		var m ModelTotals
		if err := rows.Scan(&m.Model, &m.Messages, &m.Input, &m.Output,
			&m.CacheRead, &m.CacheWrite, &m.CostUSD); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
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

// TokenBucket is one time-bucketed total of token usage.
type TokenBucket struct {
	BucketStart time.Time
	Tokens      int64
}

// TokenBuckets returns token totals grouped into fixed-size time buckets,
// oldest bucket first, for all messages at or after since.
func (c *Cache) TokenBuckets(bucketDur time.Duration, since time.Time) ([]TokenBucket, error) {
	bucketSecs := int64(bucketDur.Seconds())
	sinceStr := since.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	rows, err := c.db.Query(`
SELECT
  CAST(CAST(strftime('%s', ts) AS INTEGER) / ? AS INTEGER) * ? AS bucket_epoch,
  SUM(input_tokens + output_tokens + cache_read_tokens
      + cache_write_5m_tokens + cache_write_1h_tokens)
FROM messages
WHERE ts >= ?
GROUP BY bucket_epoch
ORDER BY bucket_epoch ASC
`, bucketSecs, bucketSecs, sinceStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenBucket
	for rows.Next() {
		var epoch, tokens int64
		if err := rows.Scan(&epoch, &tokens); err != nil {
			return nil, err
		}
		out = append(out, TokenBucket{
			BucketStart: time.Unix(epoch, 0).UTC(),
			Tokens:      tokens,
		})
	}
	return out, rows.Err()
}

func (c *Cache) LiveSessions(now time.Time, since time.Duration) ([]LiveSession, error) {
	cutoff := now.UTC().Add(-since).Format("2006-01-02T15:04:05.000Z07:00")
	rows, err := c.db.Query(`
SELECT
  session_id,
  COALESCE(NULLIF(project_canonical, ''), project_slug) AS proj,
  COALESCE(worktree_branch, '') AS wt,
  model,
  MAX(ts) AS last_ts,
  SUM(cost_usd_estimate) AS cost
FROM messages
WHERE ts >= ?
GROUP BY session_id
ORDER BY last_ts DESC
LIMIT 200
`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LiveSession
	for rows.Next() {
		var ls LiveSession
		var lastTS string
		if err := rows.Scan(&ls.SessionID, &ls.ProjectCanonical,
			&ls.WorktreeBranch, &ls.Model, &lastTS, &ls.CostUSD); err != nil {
			return nil, err
		}
		ls.LastTS, _ = time.Parse("2006-01-02T15:04:05.000Z07:00", lastTS)
		out = append(out, ls)
	}
	return out, rows.Err()
}
