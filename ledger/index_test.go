package ledger

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func legacyFixture(t *testing.T) (string, Hash, Hash) {
	t.Helper()
	c, path := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	id, err := c.Submit(signed(t, k, b, 10, 1))
	if err != nil {
		t.Fatal(err)
	}
	mustMine(t, c, a)
	pending, err := c.Submit(signed(t, k, b, 5, 2))
	if err != nil {
		t.Fatal(err)
	}
	if err = c.db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(confirmedBucket); err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("version"), key(1))
	}); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	return path, id, pending
}
func TestLegacyUpgradeAndRestore(t *testing.T) {
	path, id, pending := legacyFixture(t)
	// Restoring a legacy snapshot migrates only the restored copy.
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err = Restore(context.Background(), path, restored); err != nil {
		t.Fatal(err)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatal("restore modified source", err)
	}
	for _, p := range []string{path, restored} {
		c, err := Open(context.Background(), p, Options{})
		if err != nil {
			t.Fatal(err)
		}
		info, err := c.Lookup(id)
		if err != nil || info.Status != "confirmed" || info.Height == nil || *info.Height != 2 {
			t.Fatal(info, err)
		}
		info, err = c.Lookup(pending)
		if err != nil || info.Status != "pending" {
			t.Fatal(info, err)
		}
		if err = c.db.View(func(tx *bolt.Tx) error {
			if !bytes.Equal(tx.Bucket([]byte("meta")).Get([]byte("version")), key(StorageVersion)) {
				return ErrVersion
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err = c.Validate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err = c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestIndexCorruptionRejected(t *testing.T) {
	for _, kind := range []string{"missing bucket", "missing entry", "wrong height", "wrong position", "extra entry"} {
		t.Run(kind, func(t *testing.T) {
			c, path := newChain(t)
			a, k := identity(t)
			b, _ := identity(t)
			mustMine(t, c, a)
			id, err := c.Submit(signed(t, k, b, 1, 1))
			if err != nil {
				t.Fatal(err)
			}
			mustMine(t, c, a)
			if err = c.db.Update(func(tx *bolt.Tx) error {
				index := tx.Bucket(confirmedBucket)
				switch kind {
				case "missing bucket":
					return tx.DeleteBucket(confirmedBucket)
				case "missing entry":
					return index.Delete(id[:])
				case "wrong height":
					return index.Put(id[:], location(0, 0))
				case "wrong position":
					return index.Put(id[:], location(2, 1))
				default:
					var unknown Hash
					return index.Put(unknown[:], location(2, 0))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(context.Background(), path, Options{}); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					opened.Close()
				}
				t.Fatal(err)
			}
		})
	}
}
func TestLegacyUpgradeCancellationAndCorruption(t *testing.T) {
	source, _, _ := legacyFixture(t)
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// Count cancellation checkpoints, then cancel at the final one, after index
	// construction but before the version bump/commit. Earlier validation and
	// reconstruction checkpoints are exercised as well.
	probe := filepath.Join(t.TempDir(), "probe.db")
	if err = os.WriteFile(probe, original, 0600); err != nil {
		t.Fatal(err)
	}
	counter := &checkContext{Context: context.Background(), remaining: 1000000, result: context.Canceled}
	c, err := Open(counter, probe, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	for _, budget := range []int{0, 3, counter.checks - 1} {
		path := filepath.Join(t.TempDir(), "cancelled.db")
		if err = os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		ctx := &checkContext{Context: context.Background(), remaining: budget, result: context.Canceled}
		opened, err := Open(ctx, path, Options{})
		if !errors.Is(err, context.Canceled) {
			if opened != nil {
				opened.Close()
			}
			t.Fatal(budget, err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(unchanged, original) {
			t.Fatal("cancelled upgrade changed database", err)
		}
		db, err := bolt.Open(path, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.View(func(tx *bolt.Tx) error {
			if tx.Bucket(confirmedBucket) != nil || !bytes.Equal(tx.Bucket([]byte("meta")).Get([]byte("version")), key(1)) {
				return ErrCorrupt
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	db, err := bolt.Open(source, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("blocks")).Put(key(1), []byte("invalid")) }); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := Open(context.Background(), source, Options{}); !errors.Is(err, ErrCorrupt) {
		if c != nil {
			c.Close()
		}
		t.Fatal(err)
	}
	after, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid legacy ledger modified", err)
	}
}
func TestTouchedAccountsPreservedAndIndexRollback(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	untouched, _ := identity(t)
	mustMine(t, c, untouched)
	parent := mustMine(t, c, a)
	before, err := c.Balance(untouched)
	if err != nil {
		t.Fatal(err)
	}
	transfer := signed(t, k, b, 10, 1)
	id, err := c.Submit(transfer)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := mine(context.Background(), Block{Version: Version, NetworkID: NetworkID, Height: parent.Height + 1, Timestamp: parent.Timestamp, Previous: mustHash(t, parent), Transactions: []Transaction{transfer}, RewardTo: a, Reward: Reward, Difficulty: Difficulty})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.commit(&cancelAtCommit{Context: context.Background()}, parent, candidate, []Transaction{transfer}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = c.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(confirmedBucket).Get(id[:]) != nil {
			return ErrCorrupt
		}
		return nil
	}); err != nil {
		t.Fatal("index escaped rollback", err)
	}
	if err = c.commit(context.Background(), parent, candidate, []Transaction{transfer}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Balance(untouched)
	if err != nil || before != after {
		t.Fatal("unrelated account changed", err)
	}
	if err = c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
