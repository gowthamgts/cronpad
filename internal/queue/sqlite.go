package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/jonboulle/clockwork"
	_ "modernc.org/sqlite"
)

// sqliteSchemaVersion is stored in PRAGMA user_version. The queue is a
// rebuildable index, so any other version is dropped and recreated rather than
// migrated: the dispatcher's startup sweep repopulates it from SQLite.
const sqliteSchemaVersion = 1

const sqliteSchema = `
CREATE TABLE due (member TEXT PRIMARY KEY, at INTEGER NOT NULL) STRICT;
CREATE INDEX due_at ON due(at, member);
CREATE TABLE inflight (member TEXT PRIMARY KEY, at INTEGER NOT NULL) STRICT;
CREATE INDEX inflight_at ON inflight(at, member);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, expires_at INTEGER) STRICT;
PRAGMA user_version = 1;`

const lockKey = "lock:dispatcher"

// SQLite keeps the queue in its own database file: a due set and an in-flight
// set as two tables ordered by (at, member). A member may be in both; Ack and
// Remove clear it from each. Every mutating method is one immediate
// transaction, so a read-then-move can't interleave with another writer. Lock
// expiry uses the injected clock.
type SQLite struct {
	db    *sql.DB
	clock clockwork.Clock
}

// NewSQLite opens (or creates) the queue file. A file that cannot be opened,
// fails its integrity check or has another schema version is replaced.
func NewSQLite(ctx context.Context, path string, clock clockwork.Clock, busy time.Duration) (*SQLite, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("queue path: %w", err)
	}
	if err = os.MkdirAll(filepath.Dir(abs), 0700); err != nil {
		return nil, fmt.Errorf("queue directory: %w", err)
	}
	q, err := openSQLite(ctx, abs, clock, busy)
	if err == nil {
		return q, nil
	}
	// DECISION: the queue holds nothing that SQLite can't rebuild, so a damaged
	// file is set aside (kept for inspection) instead of blocking startup.
	aside := fmt.Sprintf("%s.corrupt-%d", abs, clock.Now().Unix())
	if rerr := os.Rename(abs, aside); rerr != nil {
		return nil, fmt.Errorf("open queue: %w (and could not move it aside: %w)", err, rerr)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Rename(abs+suffix, aside+suffix)
	}
	q, rerr := openSQLite(ctx, abs, clock, busy)
	if rerr != nil {
		return nil, fmt.Errorf("open queue: %w", rerr)
	}
	return q, nil
}

func openSQLite(ctx context.Context, abs string, clock clockwork.Clock, busy time.Duration) (_ *SQLite, err error) {
	v := url.Values{}
	v.Add("_pragma", "busy_timeout("+strconv.FormatInt(busy.Milliseconds(), 10)+")")
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "synchronous(NORMAL)")
	// Immediate transactions take the write lock up front, so two processes on
	// one file wait on busy_timeout instead of failing a read-to-write upgrade.
	v.Set("_txlock", "immediate")
	u := url.URL{Scheme: "file", Path: abs, RawQuery: v.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	defer func() {
		if err != nil {
			_ = db.Close()
		}
	}()
	var check string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return nil, err
	}
	if check != "ok" {
		return nil, fmt.Errorf("integrity check: %s", check)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version != sqliteSchemaVersion {
		if _, err = db.ExecContext(ctx, "DROP TABLE IF EXISTS due; DROP TABLE IF EXISTS inflight; DROP TABLE IF EXISTS meta;"+sqliteSchema); err != nil {
			return nil, fmt.Errorf("queue schema: %w", err)
		}
	}
	return &SQLite{db: db, clock: clock}, nil
}

// Close releases the database handle.
func (q *SQLite) Close() error { return q.db.Close() }

func (q *SQLite) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func members(ms []string) string {
	b, _ := json.Marshal(ms)
	return string(b)
}

// moveRows deletes up to limit rows from one table in (at, member) order and
// upserts them into the other, returning them in that order.
func moveRows(ctx context.Context, tx *sql.Tx, from, to string, before int64, limit int, at func(int64) int64) ([]Item, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM `+from+` WHERE member IN (SELECT member FROM `+from+` WHERE at <= ? ORDER BY at, member LIMIT ?) RETURNING member, at`, before, limit)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	for rows.Next() {
		var it Item
		var ms int64
		if err = rows.Scan(&it.Member, &ms); err != nil {
			_ = rows.Close()
			return nil, err
		}
		it.Due = time.UnixMilli(ms).UTC()
		out = append(out, it)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	// RETURNING order is unspecified; restore the sorted-set order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Due.Equal(out[j].Due) {
			return out[i].Member < out[j].Member
		}
		return out[i].Due.Before(out[j].Due)
	})
	for _, it := range out {
		if _, err = tx.ExecContext(ctx, `INSERT INTO `+to+`(member, at) VALUES(?, ?) ON CONFLICT(member) DO UPDATE SET at = excluded.at`, it.Member, at(it.Due.UnixMilli())); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (q *SQLite) Add(ctx context.Context, m string, t time.Time, nx bool) error {
	stmt := `INSERT INTO due(member, at) VALUES(?, ?) ON CONFLICT(member) DO UPDATE SET at = excluded.at`
	if nx {
		stmt = `INSERT INTO due(member, at) VALUES(?, ?) ON CONFLICT(member) DO NOTHING`
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := q.db.ExecContext(ctx, stmt, m, t.UnixMilli()); err != nil {
		return fmt.Errorf("enqueue: %w", err)
	}
	return nil
}

func (q *SQLite) Remove(ctx context.Context, m string) error {
	return q.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM due WHERE member = ?`, m); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM inflight WHERE member = ?`, m)
		return err
	})
}

func (q *SQLite) Claim(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Item, error) {
	if limit <= 0 {
		return []Item{}, nil
	}
	if lease < time.Millisecond {
		return nil, fmt.Errorf("lease must be at least 1ms")
	}
	var out []Item
	err := q.tx(ctx, func(tx *sql.Tx) error {
		var err error
		expiry := now.UnixMilli() + lease.Milliseconds()
		out, err = moveRows(ctx, tx, "due", "inflight", now.UnixMilli(), limit, func(int64) int64 { return expiry })
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("claim: %w", err)
	}
	return out, nil
}

func (q *SQLite) Ack(ctx context.Context, m, retry string, at time.Time) error {
	return q.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM inflight WHERE member = ?`, m); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM due WHERE member = ?`, m); err != nil {
			return err
		}
		if retry == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO due(member, at) VALUES(?, ?) ON CONFLICT(member) DO UPDATE SET at = excluded.at`, retry, at.UnixMilli())
		return err
	})
}

// Defer releases a claim without changing its occurrence identity.
func (q *SQLite) Defer(ctx context.Context, member string, at time.Time) error {
	return q.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM inflight WHERE member = ?`, member); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO due(member, at) VALUES(?, ?) ON CONFLICT(member) DO UPDATE SET at = excluded.at`, member, at.UnixMilli())
		return err
	})
}

func (q *SQLite) Extend(ctx context.Context, m string, at time.Time) (int, error) {
	code := 0
	err := q.tx(ctx, func(tx *sql.Tx) error {
		r, err := tx.ExecContext(ctx, `UPDATE inflight SET at = ? WHERE member = ?`, at.UnixMilli(), m)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 1 {
			code = 1
			return nil
		}
		r, err = tx.ExecContext(ctx, `DELETE FROM due WHERE member = ?`, m)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n == 0 {
			return nil
		}
		code = 2
		_, err = tx.ExecContext(ctx, `INSERT INTO inflight(member, at) VALUES(?, ?)`, m, at.UnixMilli())
		return err
	})
	return code, err
}

func (q *SQLite) reap(ctx context.Context, now time.Time, limit int) ([]Item, error) {
	if limit <= 0 {
		return []Item{}, ctx.Err()
	}
	var out []Item
	err := q.tx(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = moveRows(ctx, tx, "inflight", "due", now.UnixMilli(), limit, func(int64) int64 { return now.UnixMilli() })
		return err
	})
	return out, err
}

func (q *SQLite) Reap(ctx context.Context, now time.Time, limit int) (int, error) {
	v, err := q.reap(ctx, now, limit)
	return len(v), err
}

// ReapAndList performs the same atomic reap and reports moved members for events.
func (q *SQLite) ReapAndList(ctx context.Context, now time.Time, limit int) ([]string, error) {
	v, err := q.reap(ctx, now, limit)
	if err != nil {
		return nil, fmt.Errorf("reap members: %w", err)
	}
	out := make([]string, len(v))
	for i, it := range v {
		out[i] = it.Member
	}
	return out, nil
}

// RequeueAll moves every in-flight member back to due, in bounded batches.
func (q *SQLite) RequeueAll(ctx context.Context, now time.Time) (int, error) {
	total := 0
	for {
		var n int
		err := q.tx(ctx, func(tx *sql.Tx) error {
			v, err := moveRows(ctx, tx, "inflight", "due", math.MaxInt64, 1000, func(int64) int64 { return now.UnixMilli() })
			n = len(v)
			return err
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < 1000 {
			return total, nil
		}
	}
}

func (q *SQLite) Peek(ctx context.Context) (time.Time, bool, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, false, err
	}
	var ms int64
	err := q.db.QueryRowContext(ctx, `SELECT at FROM due ORDER BY at, member LIMIT 1`).Scan(&ms)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	return time.UnixMilli(ms).UTC(), true, nil
}

func (q *SQLite) Contains(ctx context.Context, ms []string) ([]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]bool, len(ms))
	if len(ms) == 0 {
		return out, nil
	}
	rows, err := q.db.QueryContext(ctx, `SELECT value FROM json_each(?) WHERE value IN (SELECT member FROM due) OR value IN (SELECT member FROM inflight)`, members(ms))
	if err != nil {
		return nil, err
	}
	present := map[string]bool{}
	for rows.Next() {
		var m string
		if err = rows.Scan(&m); err != nil {
			_ = rows.Close()
			return nil, err
		}
		present[m] = true
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i, m := range ms {
		out[i] = present[m]
	}
	return out, nil
}

func lockExpiry(now time.Time, ttl time.Duration) int64 {
	return now.Truncate(time.Millisecond).Add(ttl.Truncate(time.Millisecond)).UnixMilli()
}

func (q *SQLite) Acquire(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	if ttl < time.Millisecond {
		return false, fmt.Errorf("lock TTL must be at least 1ms")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := q.clock.Now()
	r, err := q.db.ExecContext(ctx, `INSERT INTO meta(key, value, expires_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, expires_at = excluded.expires_at WHERE meta.expires_at <= ?`,
		lockKey, token, lockExpiry(now, ttl), now.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (q *SQLite) Renew(ctx context.Context, token string, ttl time.Duration) (bool, error) {
	if ttl < time.Millisecond {
		return false, fmt.Errorf("lock TTL must be at least 1ms")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := q.clock.Now()
	r, err := q.db.ExecContext(ctx, `UPDATE meta SET expires_at = ? WHERE key = ? AND value = ? AND expires_at > ?`, lockExpiry(now, ttl), lockKey, token, now.UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (q *SQLite) Release(ctx context.Context, token string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r, err := q.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ? AND value = ? AND expires_at > ?`, lockKey, token, q.clock.Now().UnixMilli())
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (q *SQLite) setMeta(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (q *SQLite) getMeta(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var v string
	err := q.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (q *SQLite) SetEpoch(ctx context.Context, v string) error { return q.setMeta(ctx, "epoch", v) }
func (q *SQLite) Epoch(ctx context.Context) (string, error)    { return q.getMeta(ctx, "epoch") }

func (q *SQLite) SetHeartbeat(ctx context.Context, t time.Time) error {
	return q.setMeta(ctx, "heartbeat", strconv.FormatInt(t.UnixMilli(), 10))
}

func (q *SQLite) Heartbeat(ctx context.Context) (time.Time, error) {
	v, err := q.getMeta(ctx, "heartbeat")
	if err != nil || v == "" {
		return time.Time{}, err
	}
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("heartbeat: %w", err)
	}
	return time.UnixMilli(ms).UTC(), nil
}

// Ping verifies the queue file is readable.
func (q *SQLite) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.db.QueryRowContext(ctx, `SELECT 1`).Scan(new(int))
}

// Stats counts both tables in one statement.
func (q *SQLite) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	var s Stats
	err := q.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM due), (SELECT count(*) FROM inflight)`).Scan(&s.Due, &s.Inflight)
	return s, err
}

// ScanDue pages by rowid; members moved during a scan may be
// reported twice or missed, but members present throughout are always seen.
func (q *SQLite) ScanDue(ctx context.Context, cursor uint64) ([]string, uint64, error) {
	return q.scan(ctx, "due", cursor)
}

// ScanInflight reads a bounded cursor page without changing leases.
func (q *SQLite) ScanInflight(ctx context.Context, cursor uint64) ([]string, uint64, error) {
	return q.scan(ctx, "inflight", cursor)
}

func (q *SQLite) scan(ctx context.Context, table string, cursor uint64) ([]string, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	const page = 1000
	rows, err := q.db.QueryContext(ctx, `SELECT rowid, member FROM `+table+` WHERE rowid > ? ORDER BY rowid LIMIT ?`, int64(cursor), page)
	if err != nil {
		return nil, 0, err
	}
	out := make([]string, 0, page)
	var last int64
	for rows.Next() {
		var m string
		if err = rows.Scan(&last, &m); err != nil {
			_ = rows.Close()
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, 0, err
	}
	if len(out) < page {
		return out, 0, nil
	}
	return out, uint64(last), nil
}
