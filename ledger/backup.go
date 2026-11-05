package ledger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

// Backup writes a consistent bbolt snapshot, including pending transactions.
// The destination must not exist. Keys are not part of the database.
func (c *Chain) Backup(destination string) error {
	return writeSnapshot(destination, func(out *os.File) error {
		return c.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(out); return err })
	})
}

// Restore validates a copy of a backup before publishing a new database.
// It never opens the source as a writable database or replaces existing files.
func Restore(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup must be a regular file")
	}
	return writeSnapshot(destination, func(out *os.File) error { _, err := io.Copy(out, in); return err })
}

// A temporary sibling keeps incomplete or invalid snapshots out of the requested
// path. Link publishes atomically without replacing files, including symlinks.
func writeSnapshot(destination string, write func(*os.File) error) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%s: %w", destination, os.ErrExist)
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(destination)
	out, err := os.CreateTemp(dir, ".ledger-snapshot-*")
	if err != nil {
		return err
	}
	temporary := out.Name()
	defer os.Remove(temporary)
	defer out.Close()
	if err = write(out); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	candidate, err := Open(temporary)
	if err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	if err = candidate.Close(); err != nil {
		return err
	}
	if err = os.Link(temporary, destination); err != nil {
		return err
	}
	if err = syncSnapshotDirectory(dir); err != nil {
		return fmt.Errorf("snapshot created at %s, directory sync failed: %w", destination, err)
	}
	return nil
}
