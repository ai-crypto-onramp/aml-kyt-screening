package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/alert"
	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/screen"
	"github.com/redis/go-redis/v9"
)

// errDriver is a hermetic database/sql driver that returns errors for every
// operation. It lets us exercise the error-wrapping branches in the PG-backed
// stores without a real Postgres.
type errDriver struct{ err error }

func (d errDriver) Open(_ string) (driver.Conn, error) {
	return errConn(d), nil
}

type errConn struct{ err error }

func (c errConn) Prepare(_ string) (driver.Stmt, error)         { return nil, c.err }
func (c errConn) Close() error                                   { return nil }
func (c errConn) Begin() (driver.Tx, error)                     { return nil, c.err }
func (c errConn) PrepareContext(_ context.Context, _ string) (driver.Stmt, error) {
	return nil, c.err
}
func (c errConn) ExecContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
	return nil, c.err
}
func (c errConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return nil, c.err
}
func (c errConn) Ping(_ context.Context) error { return c.err }

var errDriverSentinel = errors.New("hermetic driver error")

func init() {
	sql.Register("errdriver", errDriver{err: errDriverSentinel})
}

func newErrDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("errdriver", "irrelevant")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func TestPGCacheGetError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	c := NewPGCache(db, time.Hour, 24*time.Hour)
	_, _, err := c.Get(context.Background(), "0x1", "ethereum")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGCacheSetError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	c := NewPGCache(db, time.Hour, 24*time.Hour)
	if err := c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "CLEAN"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestPGCacheDeleteError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	c := NewPGCache(db, time.Hour, 24*time.Hour)
	if err := c.Delete(context.Background(), "0x1", "ethereum"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPGCachePingError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	c := NewPGCache(db, time.Hour, 24*time.Hour)
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestPGCacheClose(t *testing.T) {
	db := newErrDB(t)
	c := NewPGCache(db, time.Hour, 24*time.Hour)
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestPGCacheSetFillsDefaults(t *testing.T) {
	// Use a closed db to force the Set error path AFTER CachedAt/TTL/ExpiresAt
	// defaults are populated. The error path still exercises the branch.
	db := newErrDB(t)
	defer db.Close()
	c := NewPGCache(db, time.Hour, 24*time.Hour, WithNow(func() time.Time {
		return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	}))
	// Verdict with all fields zero so Set fills defaults.
	_ = c.Set(context.Background(), Verdict{Address: "0x1", Chain: "ethereum", Exposure: "SANCTIONED"})
}

func TestPGScreenStoreGetError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGScreenStore(db)
	_, _, err := s.Get("nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGScreenStoreListByAddressError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGScreenStore(db)
	_, err := s.ListByAddress("0x1", "ethereum")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGScreenStorePutError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGScreenStore(db)
	err := s.Put(screen.ScreenRecord{ScreenID: "s1", Address: "0x1", Chain: "ethereum"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGAlertStoreGetError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGAlertStore(db)
	_, _, err := s.Get("nope")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGAlertStoreListError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGAlertStore(db)
	_, err := s.List("")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGAlertStoreUpdateError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGAlertStore(db)
	err := s.Update(alert.Alert{ID: "a1", Status: alert.StatusOpen})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPGAlertStoreCreateError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	s := NewPGAlertStore(db)
	_, err := s.Create(alert.Alert{ID: "a1", Status: alert.StatusOpen, CreatedAt: time.Now()})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMigrateError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	if err := Migrate(context.Background(), db); err == nil {
		t.Fatal("expected error from hermetic driver")
	}
}

func TestMigrateDownError(t *testing.T) {
	db := newErrDB(t)
	defer db.Close()
	if err := MigrateDown(context.Background(), db); err == nil {
		t.Fatal("expected error from hermetic driver")
	}
}

func newErrRedisClient() *redis.Client {
	// Point at a non-routable port so commands fail fast with a dial error.
	return redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond})
}

func TestRedisCacheGetError(t *testing.T) {
	c := NewRedisCache(newErrRedisClient(), time.Hour, 24*time.Hour)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _, err := c.Get(ctx, "0x1", "ethereum")
	if err == nil {
		t.Fatal("expected error from unreachable redis")
	}
}

func TestRedisCacheSetError(t *testing.T) {
	c := NewRedisCache(newErrRedisClient(), time.Hour, 24*time.Hour)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Set(ctx, Verdict{Address: "0x1", Chain: "ethereum", Exposure: "CLEAN"}); err == nil {
		t.Fatal("expected error from unreachable redis")
	}
}

func TestRedisCacheDeleteError(t *testing.T) {
	c := NewRedisCache(newErrRedisClient(), time.Hour, 24*time.Hour)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Delete(ctx, "0x1", "ethereum"); err == nil {
		t.Fatal("expected error from unreachable redis")
	}
}

func TestRedisCachePingError(t *testing.T) {
	c := NewRedisCache(newErrRedisClient(), time.Hour, 24*time.Hour)
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Ping(ctx); err == nil {
		t.Fatal("expected error from unreachable redis")
	}
}