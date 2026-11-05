package ledger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	transfer := signed(t, k, b, 10, 1)
	if _, err := c.Submit(transfer); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "ledger.backup")
	if err := c.Backup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backup)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatal(info, err)
	}
	// The live chain advances after the snapshot; restoration must preserve its
	// earlier tip and pending transaction, not the new live state.
	mustMine(t, c, a)
	restoredPath := filepath.Join(t.TempDir(), "restored.db")
	if err = Restore(context.Background(), backup, restoredPath); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(backup)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("restore modified source", err)
	}
	restored, err := Open(context.Background(), restoredPath, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	blocks, err := restored.Blocks()
	if err != nil || len(blocks) != 2 {
		t.Fatal(blocks, err)
	}
	pending, err := restored.Pending()
	if err != nil || len(pending) != 1 || pending[0] != transfer {
		t.Fatal(pending, err)
	}
	summary, err := restored.BalanceSummary(a)
	if err != nil || summary != (BalanceSummary{50, 10, 40, 0}) {
		t.Fatal(summary, err)
	}
	mustMine(t, restored, a)
	summary, err = restored.BalanceSummary(a)
	if err != nil || summary != (BalanceSummary{90, 0, 90, 1}) {
		t.Fatal(summary, err)
	}
	if err = restored.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestSnapshotFailurePreservesFiles(t *testing.T) {
	c, path := newChain(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	sentinel := []byte("preserve this file")
	if err := os.WriteFile(target, sentinel, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Backup(context.Background(), target); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), path, target); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if err := c.Backup(context.Background(), path); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(after, sentinel) {
		t.Fatal("overwrote destination", err)
	}
	t.Run("symlink destination", func(t *testing.T) {
		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" && (errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.Errno(1314))) {
				t.Skip("Windows denied symlink privilege")
			}
			t.Fatal(err)
		}
		if err := c.Backup(context.Background(), link); !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	})
	for _, kind := range []string{"empty", "truncated", "invalid genesis", "unsupported version"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			switch kind {
			case "empty":
				err = os.WriteFile(source, nil, 0600)
			case "truncated":
				err = os.WriteFile(source, []byte("not a database"), 0600)
			default:
				err = c.Backup(context.Background(), source)
				if err != nil {
					t.Fatal(err)
				}
				db, e := bolt.Open(source, 0600, nil)
				if e != nil {
					t.Fatal(e)
				}
				err = db.Update(func(tx *bolt.Tx) error {
					if kind == "unsupported version" {
						return tx.Bucket([]byte("meta")).Put([]byte("version"), key(99))
					}
					g := Genesis()
					g.Timestamp = 1
					return put(tx.Bucket([]byte("blocks")), key(0), g)
				})
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			original, e := os.ReadFile(source)
			if e != nil {
				t.Fatal(e)
			}
			destDir := t.TempDir()
			destination := filepath.Join(destDir, "restored")
			if e = Restore(context.Background(), source, destination); e == nil {
				t.Fatal("invalid backup accepted")
			}
			entries, e := os.ReadDir(destDir)
			if e != nil || len(entries) != 0 {
				t.Fatal("failed restore left files", entries, e)
			}
			data, e := os.ReadFile(source)
			if e != nil || !bytes.Equal(data, original) {
				t.Fatal("invalid source modified", e)
			}
		})
	}
}
func TestSnapshotPublicationFailure(t *testing.T) {
	for _, race := range []bool{false, true} {
		t.Run(fmt.Sprint(race), func(t *testing.T) {
			dir := t.TempDir()
			destination := filepath.Join(dir, "snapshot")
			err := writeSnapshot(context.Background(), destination, func(out *os.File) error {
				if !race {
					_, e := out.Write([]byte("partial"))
					if e != nil {
						return e
					}
					return errors.New("injected write failure")
				}
				// Create a valid snapshot and race a competing destination creator.
				c, _ := newChain(t)
				if e := c.db.View(func(tx *bolt.Tx) error { _, e := tx.WriteTo(out); return e }); e != nil {
					return e
				}
				return os.WriteFile(destination, []byte("competitor"), 0600)
			})
			if err == nil {
				t.Fatal("expected failure")
			}
			entries, e := os.ReadDir(dir)
			if e != nil {
				t.Fatal(e)
			}
			if race {
				data, e := os.ReadFile(destination)
				if e != nil || string(data) != "competitor" || len(entries) != 1 {
					t.Fatal("race overwrote file", e)
				}
			} else if len(entries) != 0 {
				t.Fatal("partial backup left files")
			}
		})
	}
}
func TestBackupDuringSubmissions(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	transactions := make([]Transaction, 20)
	for i := range transactions {
		transactions[i] = signed(t, k, b, 1, uint64(i+1))
	}
	done := make(chan error, 1)
	go func() {
		for _, tx := range transactions {
			if _, err := c.Submit(tx); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	// Always join the writer before cleanup closes the database, even on failure.
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 5; i++ {
		path := filepath.Join(t.TempDir(), "snapshot")
		if err := c.Backup(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Open(context.Background(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		p, err := snapshot.Pending()
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		summary, err := snapshot.BalanceSummary(a)
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		if err = snapshot.Close(); err != nil {
			t.Fatal(err)
		}
		if len(p) > len(transactions) || !reflect.DeepEqual(append([]Transaction{}, p...), transactions[:len(p)]) {
			t.Fatal("inconsistent queue snapshot")
		}
		if summary.Confirmed != 50 || summary.Reserved != uint64(len(p)) || summary.Available != 50-uint64(len(p)) {
			t.Fatal(summary)
		}
	}
}
func TestBalanceSummaryReservations(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	for _, tx := range []Transaction{signed(t, k, b, 10, 1), signed(t, k, a, 5, 2)} {
		if _, err := c.Submit(tx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.BalanceSummary(a)
	if err != nil || got != (BalanceSummary{50, 15, 35, 0}) {
		t.Fatal(got, err)
	}
	incoming, err := c.BalanceSummary(b)
	if err != nil || incoming != (BalanceSummary{}) {
		t.Fatal("pending incoming counted", incoming, err)
	}
	mustMine(t, c, a)
	got, err = c.BalanceSummary(a)
	if err != nil || got != (BalanceSummary{90, 0, 90, 2}) {
		t.Fatal(got, err)
	}
}

func TestBackupDuringMining(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	if _, err := c.Submit(signed(t, k, b, 10, 1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 3; i++ {
			if _, err := c.Mine(context.Background(), a); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for i := 0; i < 5; i++ {
		path := filepath.Join(t.TempDir(), "snapshot")
		if err := c.Backup(context.Background(), path); err != nil {
			t.Fatal(err)
		}
		snapshot, err := Open(context.Background(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := snapshot.Blocks()
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		pending, err := snapshot.Pending()
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		alice, err := snapshot.Balance(a)
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		bob, err := snapshot.Balance(b)
		if err != nil {
			snapshot.Close()
			t.Fatal(err)
		}
		if err = snapshot.Close(); err != nil {
			t.Fatal(err)
		}
		issued := uint64(len(blocks)-1) * Reward
		if alice.Balance+bob.Balance != issued {
			t.Fatal("snapshot balances disagree with blocks")
		}
		if len(blocks) == 2 {
			if len(pending) != 1 || bob.Balance != 0 || alice.Nonce != 0 {
				t.Fatal("mixed pre-commit state")
			}
		} else if len(pending) != 0 || bob.Balance != 10 || alice.Nonce != 1 {
			t.Fatal("mixed post-commit state")
		}
	}
}
