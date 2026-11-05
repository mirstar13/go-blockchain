package ledger

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// StorageVersion evolves independently of the signed protocol Version.
const StorageVersion uint64 = 2

var confirmedBucket = []byte("confirmed_ids")

func location(height uint64, position int) []byte {
	out := make([]byte, 16)
	binary.BigEndian.PutUint64(out, height)
	binary.BigEndian.PutUint64(out[8:], uint64(position))
	return out
}
func indexBlock(tx *bolt.Tx, block Block) error {
	index := tx.Bucket(confirmedBucket)
	if index == nil {
		return ErrCorrupt
	}
	for i, transfer := range block.Transactions {
		id, err := transfer.ID()
		if err != nil {
			return err
		}
		if index.Get(id[:]) != nil {
			return fmt.Errorf("%w: duplicate confirmed ID", ErrCorrupt)
		}
		if err = index.Put(id[:], location(block.Height, i)); err != nil {
			return err
		}
	}
	return nil
}

// Upgrade only a fully validated legacy database. Validation, rebuilding and
// the version bump share one transaction, so cancellation leaves version 1 intact.
func (c *Chain) upgrade(ctx context.Context) error {
	return c.db.Update(func(tx *bolt.Tx) error {
		if err := c.validateTx(ctx, tx, 1); err != nil {
			return err
		}
		if tx.Bucket(confirmedBucket) != nil {
			return fmt.Errorf("%w: unexpected legacy confirmed index", ErrCorrupt)
		}
		if _, err := tx.CreateBucket(confirmedBucket); err != nil {
			return err
		}
		cursor := tx.Bucket([]byte("blocks")).Cursor()
		for k, v := cursor.First(); k != nil; k, v = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var block Block
			if err := json.Unmarshal(v, &block); err != nil {
				return err
			}
			if err := indexBlock(tx, block); err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return tx.Bucket([]byte("meta")).Put([]byte("version"), key(StorageVersion))
	})
}
func (c *Chain) prepare(ctx context.Context) error {
	var legacy bool
	if err := c.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		if meta == nil {
			return ErrCorrupt
		}
		legacy = bytes.Equal(meta.Get([]byte("version")), key(1))
		return nil
	}); err != nil {
		return err
	}
	if legacy {
		return c.upgrade(ctx)
	}
	return c.Validate(ctx)
}
func readAccount(tx *bolt.Tx, address Address) (Account, error) {
	var a Account
	raw := tx.Bucket([]byte("accounts")).Get(address[:])
	if raw == nil {
		return a, nil
	}
	err := json.Unmarshal(raw, &a)
	return a, err
}

// Load only accounts involved in these transfers, not the whole ledger.
func transferAccounts(ctx context.Context, tx *bolt.Tx, transfers []Transaction, recipients bool, extra ...Address) (map[Address]Account, error) {
	state := map[Address]Account{}
	load := func(address Address) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, ok := state[address]; ok {
			return nil
		}
		a, err := readAccount(tx, address)
		if err != nil {
			return err
		}
		state[address] = a
		return nil
	}
	for _, address := range extra {
		if err := load(address); err != nil {
			return nil, err
		}
	}
	for _, transfer := range transfers {
		if err := load(transfer.Sender); err != nil {
			return nil, err
		}
		if recipients {
			if err := load(transfer.Recipient); err != nil {
				return nil, err
			}
		}
	}
	return state, nil
}
func senderState(tx *bolt.Tx, address Address) (Account, Account, int, error) {
	confirmed, err := readAccount(tx, address)
	if err != nil {
		return Account{}, Account{}, 0, err
	}
	pending, err := readPending(tx)
	if err != nil {
		return Account{}, Account{}, 0, err
	}
	own := make([]Transaction, 0)
	for _, transfer := range pending {
		if transfer.Sender == address {
			own = append(own, transfer)
		}
	}
	state := map[Address]Account{address: confirmed}
	if err = reserve(state, own); err != nil {
		return Account{}, Account{}, 0, err
	}
	return confirmed, state[address], len(own), nil
}
