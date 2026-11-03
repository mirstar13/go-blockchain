package ledger

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"time"

	bolt "go.etcd.io/bbolt"
)

var buckets = []string{"meta", "blocks", "accounts", "pending", "pending_ids"}

type Chain struct{ db *bolt.DB }

func key(n uint64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], n); return b[:] }
func put(b *bolt.Bucket, k []byte, v any) error {
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return b.Put(k, data)
}

// Init creates a new ledger containing only the deterministic genesis block.
// It refuses to overwrite any existing file.
func Init(path string) (err error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	db, e := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if e != nil {
		return e
	}
	e = db.Update(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if _, e := tx.CreateBucket([]byte(name)); e != nil {
				return e
			}
		}
		if e := tx.Bucket([]byte("meta")).Put([]byte("version"), key(Version)); e != nil {
			return e
		}
		return put(tx.Bucket([]byte("blocks")), key(0), Genesis())
	})
	closeErr := db.Close()
	if e != nil {
		return e
	}
	return closeErr
}

// Open locks an existing database and validates its complete contents.
// The caller must Close the chain. The zero value of Chain is not usable.
func Open(path string) (*Chain, error) {
	// Strip O_CREATE and inspect the opened descriptor: a failed Open must never
	// create a missing file or let bbolt initialize an existing empty file.
	db, e := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second, OpenFile: func(name string, flags int, mode os.FileMode) (*os.File, error) {
		f, err := os.OpenFile(name, flags&^os.O_CREATE, mode)
		if err != nil {
			return nil, err
		}
		info, err := f.Stat()
		if err == nil && (!info.Mode().IsRegular() || info.Size() == 0) {
			err = fmt.Errorf("%w: empty or non-regular database", ErrCorrupt)
		}
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return f, nil
	}})
	if e != nil {
		return nil, e
	}
	c := &Chain{db: db}
	if e = c.Validate(); e != nil {
		_ = db.Close()
		return nil, e
	}
	return c, nil
}

// Close releases the database file lock and resources.
func (c *Chain) Close() error { return c.db.Close() }
func readBlocks(tx *bolt.Tx) ([]Block, error) {
	var blocks []Block
	e := tx.Bucket([]byte("blocks")).ForEach(func(k, v []byte) error {
		var b Block
		if e := json.Unmarshal(v, &b); e != nil {
			return e
		}
		if !bytes.Equal(k, key(uint64(len(blocks)))) {
			return ErrCorrupt
		}
		blocks = append(blocks, b)
		return nil
	})
	return blocks, e
}
func readAccounts(tx *bolt.Tx) (map[Address]Account, error) {
	out := map[Address]Account{}
	e := tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
		if len(k) != 32 {
			return ErrCorrupt
		}
		var a Address
		copy(a[:], k)
		var s Account
		if e := json.Unmarshal(v, &s); e != nil {
			return e
		}
		out[a] = s
		return nil
	})
	return out, e
}
func readPending(tx *bolt.Tx) ([]Transaction, error) {
	var out []Transaction
	e := tx.Bucket([]byte("pending")).ForEach(func(k, v []byte) error {
		if len(k) != 8 || v == nil || binary.BigEndian.Uint64(k) == 0 {
			return ErrCorrupt
		}
		var t Transaction
		if e := json.Unmarshal(v, &t); e != nil {
			return e
		}
		out = append(out, t)
		return nil
	})
	return out, e
}

// reserve counts only confirmed funds, never pending incoming transfers.
func reserve(state map[Address]Account, pending []Transaction) error {
	for _, t := range pending {
		if e := t.Verify(); e != nil {
			return e
		}
		a := state[t.Sender]
		if a.Nonce == math.MaxUint64 {
			return ErrOverflow
		}
		if t.Nonce != a.Nonce+1 {
			return ErrNonce
		}
		if a.Balance < t.Amount {
			return ErrFunds
		}
		a.Balance -= t.Amount
		a.Nonce++
		state[t.Sender] = a
	}
	return nil
}

// Validate replays history and verifies the persisted state and pending indexes.
func (c *Chain) Validate() error {
	e := c.db.View(func(tx *bolt.Tx) error {
		for _, name := range buckets {
			if tx.Bucket([]byte(name)) == nil {
				return ErrCorrupt
			}
		}
		if !bytes.Equal(tx.Bucket([]byte("meta")).Get([]byte("version")), key(Version)) {
			return ErrVersion
		}
		blocks, e := readBlocks(tx)
		if e != nil {
			return e
		}
		if len(blocks) == 0 || !reflect.DeepEqual(blocks[0], Genesis()) {
			return ErrCorrupt
		}
		state := map[Address]Account{}
		for i := 1; i < len(blocks); i++ {
			if e := applyBlock(state, blocks[i-1], blocks[i]); e != nil {
				return fmt.Errorf("block %d: %w", i, e)
			}
		}
		stored, e := readAccounts(tx)
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(state, stored) {
			return fmt.Errorf("%w: account index", ErrCorrupt)
		}
		pending, e := readPending(tx)
		if e != nil {
			return e
		}
		if e = reserve(state, pending); e != nil {
			return e
		}
		ids := tx.Bucket([]byte("pending_ids"))
		count := 0
		e = tx.Bucket([]byte("pending")).ForEach(func(k, v []byte) error {
			var t Transaction
			if e := json.Unmarshal(v, &t); e != nil {
				return e
			}
			id, e := t.ID()
			if e != nil {
				return e
			}
			if !bytes.Equal(ids.Get(id[:]), k) {
				return ErrCorrupt
			}
			if binary.BigEndian.Uint64(k) > tx.Bucket([]byte("pending")).Sequence() {
				return ErrCorrupt
			}
			count++
			return nil
		})
		if e != nil {
			return e
		}
		n := 0
		if e = ids.ForEach(func(k, v []byte) error { n++; return nil }); e != nil {
			return e
		}
		if n != count {
			return ErrCorrupt
		}
		return nil
	})
	if e != nil {
		return fmt.Errorf("validate ledger: %w: %w", ErrCorrupt, e)
	}
	return nil
}

// Blocks returns independently owned blocks in ascending height order.
func (c *Chain) Blocks() ([]Block, error) {
	var out []Block
	e := c.db.View(func(tx *bolt.Tx) error { var e error; out, e = readBlocks(tx); return e })
	return out, e
}

// Pending returns independently owned transactions in arrival order.
func (c *Chain) Pending() ([]Transaction, error) {
	var out []Transaction
	e := c.db.View(func(tx *bolt.Tx) error { var e error; out, e = readPending(tx); return e })
	return out, e
}

// Balance returns confirmed state; an unknown account has zero balance and nonce.
func (c *Chain) Balance(a Address) (Account, error) {
	var out Account
	e := c.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte("accounts")).Get(a[:])
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &out)
	})
	return out, e
}

// NextNonce includes pending outgoing transactions. Submit remains the atomic
// authority if another goroutine submits before the caller uses this nonce.
func (c *Chain) NextNonce(a Address) (uint64, error) {
	var n uint64
	e := c.db.View(func(tx *bolt.Tx) error {
		s, e := readAccounts(tx)
		if e != nil {
			return e
		}
		p, e := readPending(tx)
		if e != nil {
			return e
		}
		if e = reserve(s, p); e != nil {
			return e
		}
		if s[a].Nonce == math.MaxUint64 {
			return ErrOverflow
		}
		n = s[a].Nonce + 1
		return nil
	})
	return n, e
}

// Submit verifies and atomically queues a signed transfer, reserving confirmed funds.
func (c *Chain) Submit(t Transaction) (Hash, error) {
	id, e := t.ID()
	if e != nil {
		return id, e
	}
	e = c.db.Update(func(tx *bolt.Tx) error {
		ids := tx.Bucket([]byte("pending_ids"))
		if ids.Get(id[:]) != nil {
			return ErrDuplicate
		}
		s, e := readAccounts(tx)
		if e != nil {
			return e
		}
		p, e := readPending(tx)
		if e != nil {
			return e
		}
		if e = reserve(s, append(p, t)); e != nil {
			return e
		}
		b := tx.Bucket([]byte("pending"))
		if b.Sequence() == math.MaxUint64 {
			return ErrOverflow
		}
		seq, e := b.NextSequence()
		if e != nil {
			return e
		}
		if e = put(b, key(seq), t); e != nil {
			return e
		}
		return ids.Put(id[:], key(seq))
	})
	return id, e
}

// Mine creates and commits one block, or leaves storage unchanged on error.
// It returns ErrConflict if another miner advanced the snapshot before commit.
func (c *Chain) Mine(ctx context.Context, to Address) (Block, error) {
	var parent Block
	var selected []Transaction
	e := c.db.View(func(tx *bolt.Tx) error {
		tip, e := readTip(tx)
		if e != nil {
			return e
		}
		parent = tip
		p, e := readPending(tx)
		if e != nil {
			return e
		}
		s, e := readAccounts(tx)
		if e != nil {
			return e
		}
		if e = reserve(s, p); e != nil {
			return e
		}
		if len(p) > MaxTransactions {
			p = p[:MaxTransactions]
		}
		selected = p
		return nil
	})
	if e != nil {
		return Block{}, e
	}
	if parent.Height == math.MaxUint64 {
		return Block{}, ErrOverflow
	}
	h, e := parent.Hash()
	if e != nil {
		return Block{}, e
	}
	now := uint64(time.Now().Unix())
	if now < parent.Timestamp {
		now = parent.Timestamp
	}
	b, e := mine(ctx, Block{Version: Version, NetworkID: NetworkID, Height: parent.Height + 1, Timestamp: now, Previous: h, Transactions: selected, RewardTo: to, Reward: Reward, Difficulty: Difficulty})
	if e != nil {
		return Block{}, e
	}
	e = c.commit(ctx, parent, b, selected)
	if e != nil {
		return Block{}, e
	}
	return b, nil
}

// commit atomically checks the mining snapshot and publishes all resulting state.
func (c *Chain) commit(ctx context.Context, parent, b Block, selected []Transaction) error {
	h, err := parent.Hash()
	if err != nil {
		return err
	}
	return c.db.Update(func(tx *bolt.Tx) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		last, e := readTip(tx)
		if e != nil {
			return e
		}
		tip, e := last.Hash()
		if e != nil {
			return e
		}
		if tip != h {
			return ErrConflict
		}
		p, e := readPending(tx)
		if e != nil {
			return e
		}
		if len(p) < len(selected) {
			return ErrConflict
		}
		for i := range selected {
			if p[i] != selected[i] {
				return ErrConflict
			}
		}
		s, e := readAccounts(tx)
		if e != nil {
			return e
		}
		if e = applyBlock(s, parent, b); e != nil {
			return e
		}
		if e = put(tx.Bucket([]byte("blocks")), key(b.Height), b); e != nil {
			return e
		}
		for a, account := range s {
			if e = put(tx.Bucket([]byte("accounts")), a[:], account); e != nil {
				return e
			}
		}
		pending := tx.Bucket([]byte("pending"))
		ids := tx.Bucket([]byte("pending_ids"))
		for _, t := range selected {
			id, e := t.ID()
			if e != nil {
				return e
			}
			k := append([]byte(nil), ids.Get(id[:])...)
			if len(k) != 8 {
				return ErrCorrupt
			}
			if e = pending.Delete(k); e != nil {
				return e
			}
			if e = ids.Delete(id[:]); e != nil {
				return e
			}
		}
		return ctx.Err()
	})
}

// readTip reads only the last block; full replay remains mandatory in Open.
func readTip(tx *bolt.Tx) (Block, error) {
	k, raw := tx.Bucket([]byte("blocks")).Cursor().Last()
	if len(k) != 8 || raw == nil {
		return Block{}, ErrCorrupt
	}
	var block Block
	if err := json.Unmarshal(raw, &block); err != nil {
		return Block{}, err
	}
	if block.Height != binary.BigEndian.Uint64(k) {
		return Block{}, ErrCorrupt
	}
	return block, nil
}
