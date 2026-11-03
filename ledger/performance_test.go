package ledger

import (
	"fmt"
	bolt "go.etcd.io/bbolt"
	"path/filepath"
	"testing"
)

// Isolate storage traversal cost; these synthetic block records are not a
// consensus-valid chain and do not benchmark proof of work or reopening.
func BenchmarkTipRead(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "fixture.db")
			if err := Init(path); err != nil {
				b.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			if err = db.Update(func(tx *bolt.Tx) error {
				for i := 1; i < count; i++ {
					block := Genesis()
					block.Height = uint64(i)
					if err := put(tx.Bucket([]byte("blocks")), key(uint64(i)), block); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err = db.View(func(tx *bolt.Tx) error {
					block, err := readTip(tx)
					if err == nil && block.Height != uint64(count-1) {
						b.Fatal("wrong tip")
					}
					return err
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
