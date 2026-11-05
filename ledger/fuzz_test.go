package ledger

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// Generate valid signatures so fuzzing reaches arithmetic and nonce transitions,
// rather than spending every iteration rejecting random signatures.
func FuzzTransferState(f *testing.F) {
	f.Add(uint64(50), uint64(0), uint64(10), uint64(0), uint64(1), false, false)
	f.Add(uint64(50), uint64(math.MaxUint64), uint64(1), uint64(0), uint64(1), false, false)
	f.Add(uint64(50), uint64(0), uint64(10), uint64(math.MaxUint64), uint64(0), true, false)
	f.Add(uint64(50), uint64(0), uint64(10), uint64(0), uint64(1), false, true)
	f.Fuzz(func(t *testing.T, balance, recipientBalance, amount, nonce, txNonce uint64, self, tamper bool) {
		key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
		var sender, recipient Address
		copy(sender[:], key.Public().(ed25519.PublicKey))
		recipient[0] = 1
		if self {
			recipient = sender
		}
		tx := signed(t, key, recipient, amount, txNonce)
		if tamper {
			tx.Signature[0] ^= 1
		}
		state := map[Address]Account{recipient: {recipientBalance, 0}, sender: {balance, nonce}}
		before := map[Address]Account{}
		for a, v := range state {
			before[a] = v
		}
		err := apply(state, tx)
		valid := !tamper && amount > 0 && nonce < math.MaxUint64 && txNonce == nonce+1 && amount <= balance && (self || recipientBalance <= math.MaxUint64-amount)
		if (err == nil) != valid {
			t.Fatalf("valid=%v error=%v", valid, err)
		}
		if err != nil {
			if !reflect.DeepEqual(state, before) {
				t.Fatal("rejection changed state")
			}
			return
		}
		if state[sender].Nonce != nonce+1 {
			t.Fatal("nonce not advanced once")
		}
		if self {
			if state[sender].Balance != balance {
				t.Fatal("self transfer changed balance")
			}
		} else if state[sender].Balance != balance-amount || state[recipient].Balance != recipientBalance+amount {
			t.Fatal("incorrect transfer accounting")
		}
		after := map[Address]Account{}
		for a, v := range state {
			after[a] = v
		}
		if err := apply(state, tx); err == nil || !reflect.DeepEqual(state, after) {
			t.Fatal("replay accepted or mutated state")
		}
	})
}

// Fuzz application records inside structurally valid bbolt files. Random bbolt
// page corruption is outside this test: bbolt uses memory-mapped page traversal.
func FuzzStoredRecords(f *testing.F) {
	genesis, _ := json.Marshal(Genesis())
	f.Add(uint8(0), genesis)
	f.Add(uint8(0), []byte(`null`))
	f.Add(uint8(1), []byte(`{"NetworkID":"go-blockchain-v1","Amount":18446744073709551616}`))
	f.Add(uint8(2), []byte(`{"Balance":-1}`))
	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > 65536 {
			t.Skip()
		}
		path := filepath.Join(t.TempDir(), "fuzz.db")
		if err := Init(path); err != nil {
			t.Fatal(err)
		}
		db, err := bolt.Open(path, 0600, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = db.Update(func(tx *bolt.Tx) error {
			switch kind % 3 {
			case 0:
				return tx.Bucket([]byte("blocks")).Put(key(0), data)
			case 1:
				return tx.Bucket([]byte("pending")).Put(key(1), data)
			default:
				return tx.Bucket([]byte("accounts")).Put(make([]byte, 32), data)
			}
		})
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		c, err := Open(context.Background(), path, Options{})
		if err != nil {
			return
		}
		defer c.Close()
		// Any accepted mutation must still describe exactly the empty genesis ledger.
		if err = c.Validate(context.Background()); err != nil {
			t.Fatal("accepted database fails repeat validation", err)
		}
		blocks, err := c.Blocks()
		if err != nil || len(blocks) != 1 || !reflect.DeepEqual(blocks[0], Genesis()) {
			t.Fatal("invalid genesis accepted")
		}
		pending, err := c.Pending()
		if err != nil || len(pending) != 0 {
			t.Fatal("unfunded pending transaction accepted")
		}
	})
}
