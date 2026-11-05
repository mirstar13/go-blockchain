package ledger

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

// Backup writes a consistent bbolt snapshot, including pending transactions.
// The destination must not exist. Keys are not part of the database.
func (c *Chain) Backup(ctx context.Context, destination string) error {
	return writeSnapshot(ctx, destination, func(out *os.File) error {
		return c.db.View(func(tx *bolt.Tx) error { _, err := tx.WriteTo(contextWriter{ctx: ctx, out: out}); return err })
	})
}

// Restore validates a copy of a backup before publishing a new database.
// It never opens the source as a writable database or replaces existing files.
func Restore(ctx context.Context, source, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	return writeSnapshot(ctx, destination, func(out *os.File) error { _, err := io.Copy(contextWriter{ctx: ctx, out: out}, in); return err })
}

// A temporary sibling keeps incomplete or invalid snapshots out of the requested
// path. Link publishes atomically without replacing files, including symlinks.
func writeSnapshot(ctx context.Context, destination string, write func(*os.File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	candidate, err := Open(ctx, temporary, Options{})
	if err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	if err = candidate.Close(); err != nil {
		return err
	}
	return publishSnapshot(ctx, temporary, destination, syncSnapshotDirectory)
}

// Publication is irreversible: after Link succeeds, finish directory syncing
// even when the context is cancelled. The sync function is platform-specific.
func publishSnapshot(ctx context.Context, temporary, destination string, syncDirectory func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Link(temporary, destination); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("snapshot created at %s, directory sync failed: %w", destination, err)
	}
	return nil
}

// Bound each underlying write so context checks also run when WriteTo or Copy
// submits one large buffer. Blocking filesystem calls are not preemptible.
type contextWriter struct {
	ctx context.Context
	out io.Writer
}

func (w contextWriter) Write(data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		size := len(data)
		if size > 32*1024 {
			size = 32 * 1024
		}
		n, err := w.out.Write(data[:size])
		total += n
		data = data[n:]
		if err != nil {
			return total, err
		}
		if n != size {
			return total, io.ErrShortWrite
		}
	}
	return total, w.ctx.Err()
}
