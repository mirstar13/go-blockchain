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

// Base buckets are shared by storage versions 1 and 2; v2 also requires confirmed_ids.
var buckets = []string{"meta", "blocks", "accounts", "pending", "pending_ids"}

type Chain struct {
	db      *bolt.DB
	options Options
}

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
		if e := tx.Bucket([]byte("meta")).Put([]byte("version"), key(StorageVersion)); e != nil {
			return e
		}
		if _, err := tx.CreateBucket(confirmedBucket); err != nil {
			return err
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
func Open(ctx context.Context, path string, options Options) (*Chain, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
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
	c := &Chain{db: db, options: options}
	if e = c.prepare(ctx); e != nil {
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
func readPending(tx *bolt.Tx) ([]Transaction, error) {
	return readPendingContext(context.Background(), tx)
}
func readPendingContext(ctx context.Context, tx *bolt.Tx) ([]Transaction, error) {
	var out []Transaction
	e := tx.Bucket([]byte("pending")).ForEach(func(k, v []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
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
	return reserveContext(context.Background(), state, pending)
}
func reserveContext(ctx context.Context, state map[Address]Account, pending []Transaction) error {
	for _, t := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
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

// Validate replays history with cooperative cancellation. The replay retains
// account state, but only one block at a time. Admission limits do not apply.
func (c *Chain) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.db.View(func(tx *bolt.Tx) error { return c.validateTx(ctx, tx, StorageVersion) })
}

func (c *Chain) validateTx(ctx context.Context, tx *bolt.Tx, version uint64) error {
	bad := func(category string, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %s: %w", ErrCorrupt, category, err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range buckets {
		if tx.Bucket([]byte(name)) == nil {
			return bad(name, ErrInvalid)
		}
	}
	if !bytes.Equal(tx.Bucket([]byte("meta")).Get([]byte("version")), key(version)) {
		return bad("storage version", ErrVersion)
	}
	confirmed := tx.Bucket(confirmedBucket)
	if version == StorageVersion && confirmed == nil {
		return bad("confirmed index", ErrInvalid)
	}
	confirmedCount := 0
	state := map[Address]Account{}
	cursor := tx.Bucket([]byte("blocks")).Cursor()
	var parent Block
	var height uint64
	found := false
	for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		category := fmt.Sprintf("block %d", height)
		if !bytes.Equal(k, key(height)) {
			return bad(category, ErrInvalid)
		}
		var block Block
		if err := json.Unmarshal(v, &block); err != nil {
			return bad(category, err)
		}
		if height == 0 {
			if !reflect.DeepEqual(block, Genesis()) {
				return bad("genesis", ErrInvalid)
			}
		} else if err := applyBlockContext(ctx, state, parent, block); err != nil {
			return bad(category, err)
		}
		if version == StorageVersion {
			for i, transfer := range block.Transactions {
				if err := ctx.Err(); err != nil {
					return err
				}
				id, err := transfer.ID()
				if err != nil {
					return bad(category, err)
				}
				if !bytes.Equal(confirmed.Get(id[:]), location(block.Height, i)) {
					return bad("confirmed index", ErrInvalid)
				}
				confirmedCount++
			}
		}
		found = true
		parent = block
		if height == math.MaxUint64 {
			return bad(category, ErrOverflow)
		}
		height++
	}
	if !found {
		return bad("genesis", ErrInvalid)
	}
	if version == StorageVersion {
		total := 0
		err := confirmed.ForEach(func(k, v []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			total++
			return nil
		})
		if err != nil {
			return err
		}
		if total != confirmedCount {
			return bad("confirmed index", ErrInvalid)
		}
	}
	// Compare accounts incrementally rather than building a second account map.
	count := 0
	err := tx.Bucket([]byte("accounts")).ForEach(func(k, v []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(k) != 32 {
			return bad("account key", ErrInvalid)
		}
		var address Address
		copy(address[:], k)
		var account Account
		if err := json.Unmarshal(v, &account); err != nil {
			return bad("account record", err)
		}
		expected, ok := state[address]
		if !ok || expected != account {
			return bad("account index", ErrInvalid)
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	if count != len(state) {
		return bad("account index", ErrInvalid)
	}
	ids := tx.Bucket([]byte("pending_ids"))
	pending := tx.Bucket([]byte("pending"))
	count = 0
	err = pending.ForEach(func(k, v []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(k) != 8 || binary.BigEndian.Uint64(k) == 0 || binary.BigEndian.Uint64(k) > pending.Sequence() {
			return bad("pending sequence", ErrInvalid)
		}
		var transfer Transaction
		if err := json.Unmarshal(v, &transfer); err != nil {
			return bad("pending record", err)
		}
		if err := reserveContext(ctx, state, []Transaction{transfer}); err != nil {
			return bad("pending transaction", err)
		}
		id, err := transfer.ID()
		if err != nil {
			return bad("pending ID", err)
		}
		if !bytes.Equal(ids.Get(id[:]), k) {
			return bad("pending ID index", ErrInvalid)
		}
		count++
		return nil
	})
	if err != nil {
		return err
	}
	indexed := 0
	err = ids.ForEach(func(k, v []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		indexed++
		return nil
	})
	if err != nil {
		return err
	}
	if indexed != count {
		return bad("pending ID index", ErrInvalid)
	}
	return ctx.Err()
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
	var nonce uint64
	err := c.db.View(func(tx *bolt.Tx) error {
		_, reserved, _, err := senderState(tx, a)
		if err != nil {
			return err
		}
		if reserved.Nonce == math.MaxUint64 {
			return ErrOverflow
		}
		nonce = reserved.Nonce + 1
		return nil
	})
	return nonce, err
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
		p, e := readPending(tx)
		if e != nil {
			return e
		}
		s, e := transferAccounts(context.Background(), tx, p, false, t.Sender)
		if e != nil {
			return e
		}
		if e = reserve(s, append(p, t)); e != nil {
			return e
		}
		if len(p) >= c.options.MaxPending {
			return &QueueFullError{Scope: "global", Limit: c.options.MaxPending, Count: len(p)}
		}
		senderCount := 0
		for _, queued := range p {
			if queued.Sender == t.Sender {
				senderCount++
			}
		}
		if senderCount >= c.options.MaxPendingPerSender {
			return &QueueFullError{Scope: "sender", Limit: c.options.MaxPendingPerSender, Count: senderCount}
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
		p, e := readPendingContext(ctx, tx)
		if e != nil {
			return e
		}
		s, e := transferAccounts(ctx, tx, p, false)
		if e != nil {
			return e
		}
		if e = reserveContext(ctx, s, p); e != nil {
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
		s, e := transferAccounts(context.Background(), tx, b.Transactions, true, b.RewardTo)
		if e != nil {
			return e
		}
		if e = applyBlock(s, parent, b); e != nil {
			return e
		}
		if e = put(tx.Bucket([]byte("blocks")), key(b.Height), b); e != nil {
			return e
		}
		if err := indexBlock(tx, b); err != nil {
			return err
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
