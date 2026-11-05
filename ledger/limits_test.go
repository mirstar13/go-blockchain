package ledger

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func sequence(t *testing.T, c *Chain) uint64 {
	t.Helper()
	var n uint64
	if err := c.db.View(func(tx *bolt.Tx) error { n = tx.Bucket([]byte("pending")).Sequence(); return nil }); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestAdmissionLimits(t *testing.T) {
	for _, scope := range []string{"global", "sender"} {
		t.Run(scope, func(t *testing.T) {
			c, _ := newChain(t)
			a, k := identity(t)
			b, bk := identity(t)
			mustMine(t, c, a)
			mustMine(t, c, b)
			c.options = Options{MaxPending: 2, MaxPendingPerSender: 1}
			first := signed(t, k, b, 1, 1)
			if _, err := c.Submit(first); err != nil {
				t.Fatal(err)
			}
			if scope == "global" {
				if _, err := c.Submit(signed(t, bk, a, 1, 1)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := c.Pending()
			seq := sequence(t, c)
			_, err := c.Submit(signed(t, k, b, 1, 2))
			var full *QueueFullError
			if !errors.Is(err, ErrQueueFull) || !errors.As(err, &full) || full.Scope != scope {
				t.Fatal(err)
			}
			if full.Count != full.Limit {
				t.Fatal(full)
			}
			after, _ := c.Pending()
			if !reflect.DeepEqual(before, after) || seq != sequence(t, c) {
				t.Fatal("rejection mutated queue")
			}
			if _, err = c.Submit(first); !errors.Is(err, ErrDuplicate) {
				t.Fatal("duplicate precedence", err)
			}
		})
	}
}
func TestOptionsValidation(t *testing.T) {
	for _, o := range []Options{{-1, 1}, {1, -1}, {2, 3}, {1, 0}} {
		if _, err := o.normalized(); err == nil {
			t.Fatal(o)
		}
	}
	if o, err := (Options{}).normalized(); err != nil || o != (Options{1000, 100}) {
		t.Fatal(o, err)
	}
}
func TestConcurrentLastSlot(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, bk := identity(t)
	mustMine(t, c, a)
	mustMine(t, c, b)
	c.options = Options{1, 1}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, tx := range []Transaction{signed(t, k, b, 1, 1), signed(t, bk, a, 1, 1)} {
		wg.Add(1)
		go func(tx Transaction) { defer wg.Done(); _, err := c.Submit(tx); out <- err }(tx)
	}
	wg.Wait()
	close(out)
	success := 0
	for err := range out {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrQueueFull) {
			t.Fatal(err)
		}
	}
	if success != 1 || sequence(t, c) != 1 {
		t.Fatal("capacity race", success)
	}
}
func TestOversizedQueueDrains(t *testing.T) {
	c, path := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	var ids []Hash
	for n := uint64(1); n <= 3; n++ {
		id, err := c.Submit(signed(t, k, b, 1, n))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	c, err = Open(context.Background(), path, Options{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(signed(t, k, b, 1, 4)); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "snapshot")
	if err = c.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err = Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(context.Background(), restored, Options{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	mustMine(t, copy, a)
	r, err := c.PrepareCancellation(ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Cancel(authorize(k, r)); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(signed(t, k, b, 1, 2)); !errors.Is(err, ErrQueueFull) {
		t.Fatal(err)
	}
	mustMine(t, c, a)
	if _, err = c.Submit(signed(t, k, b, 1, 2)); err != nil {
		t.Fatal(err)
	}
}
