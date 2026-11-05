package ledger

import (
	"encoding/binary"
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
					if err := indexBlock(tx, block); err != nil {
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

// Synthetic records isolate query cost; these fixtures are not valid chains.
func BenchmarkAccountSummary(b *testing.B) {
	for _, count := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "accounts.db")
			if err := Init(path); err != nil {
				b.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			var address Address
			if err = db.Update(func(tx *bolt.Tx) error {
				for i := 0; i < count; i++ {
					binary.BigEndian.PutUint64(address[:8], uint64(i))
					if err := put(tx.Bucket([]byte("accounts")), address[:], Account{Balance: 50}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			c := &Chain{db: db}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				summary, err := c.BalanceSummary(address)
				if err != nil || summary.Confirmed != 50 {
					b.Fatal(summary, err)
				}
			}
		})
	}
}
func BenchmarkConfirmedLookup(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "lookup.db")
			if err := Init(path); err != nil {
				b.Fatal(err)
			}
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			var id Hash
			if err = db.Update(func(tx *bolt.Tx) error {
				for i := 1; i <= count; i++ {
					transfer := Transaction{NetworkID: NetworkID, Amount: 1, Nonce: uint64(i)}
					id, _ = transfer.ID()
					block := Genesis()
					block.Height = uint64(i)
					block.Transactions = []Transaction{transfer}
					if err := put(tx.Bucket([]byte("blocks")), key(uint64(i)), block); err != nil {
						return err
					}
					if err := indexBlock(tx, block); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			c := &Chain{db: db}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				info, err := c.Lookup(id)
				if err != nil || info.Height == nil || *info.Height != uint64(count) {
					b.Fatal(info, err)
				}
			}
		})
	}
}
