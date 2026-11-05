package ledger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// A deterministic cooperative-cancellation schedule, with no timer races.
type checkContext struct {
	context.Context
	remaining int
	checks    int
	result    error
}

func (c *checkContext) Err() error {
	c.checks++
	if c.remaining == 0 {
		return c.result
	}
	c.remaining--
	return nil
}
func TestMaintenanceCancellation(t *testing.T) {
	c, path := newChain(t)
	a, k := identity(t)
	mustMine(t, c, a)
	if _, err := c.Submit(signed(t, k, a, 1, 1)); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, budget := range []int{0, 3, 8} {
			ctx := &checkContext{Context: context.Background(), remaining: budget, result: cause}
			err := c.Validate(ctx)
			if !errors.Is(err, cause) || errors.Is(err, ErrCorrupt) {
				t.Fatalf("budget %d: %v", budget, err)
			}
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := &checkContext{Context: context.Background(), remaining: 3, result: context.Canceled}
	if opened, err := Open(ctx, path, Options{}); err == nil {
		opened.Close()
		t.Fatal("cancelled open succeeded")
	} else if !errors.Is(err, context.Canceled) || errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	opened, err := Open(context.Background(), path, Options{})
	if err != nil {
		t.Fatal("open leaked lock", err)
	}
	defer opened.Close()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "absent")
	if err = opened.Backup(cancelled, dest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = Restore(cancelled, path, dest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("cancelled operation published")
	}
	if err = c.Validate(context.Background()); !errors.Is(err, bolt.ErrDatabaseNotOpen) || errors.Is(err, ErrCorrupt) {
		t.Fatal("operational error mislabeled", err)
	}
	if _, err = Open(context.Background(), filepath.Join(t.TempDir(), "missing"), Options{}); !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

type cancelWriter struct {
	cancel context.CancelFunc
	calls  int
}

func (w *cancelWriter) Write(p []byte) (int, error) { w.calls++; w.cancel(); return len(p), nil }
func TestCopyAndPublicationCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &cancelWriter{cancel: cancel}
	n, err := (contextWriter{ctx: ctx, out: sink}).Write(make([]byte, 100000))
	if !errors.Is(err, context.Canceled) || n != 32768 || sink.calls != 1 {
		t.Fatal(n, err, sink.calls)
	}
	c, _ := newChain(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "snapshot")
	ctx, cancel = context.WithCancel(context.Background())
	err = writeSnapshot(ctx, dest, func(out *os.File) error {
		err := c.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(out); return err })
		cancel()
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("temporary output leaked", entries, err)
	}
	temp := filepath.Join(dir, "temp")
	if err = os.WriteFile(temp, []byte("validated fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = publishSnapshot(ctx, temp, dest, func(string) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	err = publishSnapshot(ctx, temp, dest, func(string) error { cancel(); return nil })
	if err != nil {
		t.Fatal("publication reported cancellation", err)
	}
	if _, err = os.Stat(dest); err != nil {
		t.Fatal(err)
	}
}
func TestSnapshotValidationCancellation(t *testing.T) {
	c, _ := newChain(t)
	a, _ := identity(t)
	mustMine(t, c, a)
	var snapshot bytes.Buffer
	if err := c.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(&snapshot); return err }); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx := &checkContext{Context: context.Background(), remaining: 5, result: context.Canceled}
	err := writeSnapshot(ctx, filepath.Join(dir, "out"), func(f *os.File) error { _, err := io.Copy(f, bytes.NewReader(snapshot.Bytes())); return err })
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
