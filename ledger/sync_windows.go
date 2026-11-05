package ledger

// Windows does not support syncing directory handles via os.File.Sync. Snapshot
// contents are synced before atomic NTFS publication; power-loss durability of
// the directory entry is not guaranteed.
func syncSnapshotDirectory(string) error { return nil }
