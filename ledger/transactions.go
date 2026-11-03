package ledger

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrNotFound  = errors.New("transaction not found")
	ErrConfirmed = errors.New("confirmed transactions cannot be cancelled")
)

func ParseHash(value string) (Hash, error) {
	var h Hash
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != len(h) {
		return h, errors.New("transaction ID must be 64 hexadecimal characters")
	}
	copy(h[:], raw)
	return h, nil
}

type TransactionInfo struct {
	Transaction Transaction
	Status      string
	Height      *uint64
}

func lookup(tx *bolt.Tx, id Hash) (TransactionInfo, error) {
	if sequence := tx.Bucket([]byte("pending_ids")).Get(id[:]); sequence != nil {
		var t Transaction
		if err := json.Unmarshal(tx.Bucket([]byte("pending")).Get(sequence), &t); err != nil {
			return TransactionInfo{}, err
		}
		return TransactionInfo{Transaction: t, Status: "pending"}, nil
	}
	cursor := tx.Bucket([]byte("blocks")).Cursor()
	for _, raw := cursor.First(); raw != nil; _, raw = cursor.Next() {
		var block Block
		if err := json.Unmarshal(raw, &block); err != nil {
			return TransactionInfo{}, err
		}
		for _, t := range block.Transactions {
			got, err := t.ID()
			if err != nil {
				return TransactionInfo{}, err
			}
			if got == id {
				height := block.Height
				return TransactionInfo{Transaction: t, Status: "confirmed", Height: &height}, nil
			}
		}
	}
	return TransactionInfo{}, ErrNotFound
}

// Lookup reports current pending/confirmed status. Cancellation leaves no tombstone.
func (c *Chain) Lookup(id Hash) (TransactionInfo, error) {
	var info TransactionInfo
	err := c.db.View(func(tx *bolt.Tx) error { var err error; info, err = lookup(tx, id); return err })
	return info, err
}

// Cancellation binds authorization to one admission of a transaction. The queue
// sequence prevents replay after the same signed transfer is submitted again.
type Cancellation struct {
	ID        Hash
	Sender    Address
	Sequence  uint64
	Signature [64]byte
}

func (r Cancellation) SigningBytes() []byte {
	var b bytes.Buffer
	field(&b, "ledger/cancel/v1")
	field(&b, NetworkID)
	b.Write(r.ID[:])
	b.Write(r.Sender[:])
	number(&b, r.Sequence)
	return b.Bytes()
}
func (c *Chain) PrepareCancellation(id Hash) (Cancellation, error) {
	var request Cancellation
	err := c.db.View(func(tx *bolt.Tx) error {
		info, err := lookup(tx, id)
		if err != nil {
			return err
		}
		if info.Status == "confirmed" {
			return ErrConfirmed
		}
		sequence := tx.Bucket([]byte("pending_ids")).Get(id[:])
		if len(sequence) != 8 {
			return ErrCorrupt
		}
		request = Cancellation{ID: id, Sender: info.Transaction.Sender, Sequence: binary.BigEndian.Uint64(sequence)}
		return nil
	})
	return request, err
}

// Cancel atomically removes the target and every later queued nonce of its sender.
// A concurrent miner either wins first or observes a changed queue and conflicts.
func (c *Chain) Cancel(request Cancellation) ([]Hash, error) {
	if !ed25519.Verify(request.Sender[:], request.SigningBytes(), request.Signature[:]) {
		return nil, ErrSignature
	}
	var removed []Hash
	err := c.db.Update(func(tx *bolt.Tx) error {
		info, err := lookup(tx, request.ID)
		if err != nil {
			return err
		}
		if info.Status == "confirmed" {
			return ErrConfirmed
		}
		if info.Transaction.Sender != request.Sender {
			return ErrSignature
		}
		ids := tx.Bucket([]byte("pending_ids"))
		sequence := ids.Get(request.ID[:])
		if !bytes.Equal(sequence, key(request.Sequence)) {
			return ErrConflict
		}
		pending, err := readPending(tx)
		if err != nil {
			return err
		}
		for _, t := range pending {
			if t.Sender != request.Sender || t.Nonce < info.Transaction.Nonce {
				continue
			}
			id, err := t.ID()
			if err != nil {
				return err
			}
			k := append([]byte(nil), ids.Get(id[:])...)
			if len(k) != 8 {
				return ErrCorrupt
			}
			if err = tx.Bucket([]byte("pending")).Delete(k); err != nil {
				return err
			}
			if err = ids.Delete(id[:]); err != nil {
				return err
			}
			removed = append(removed, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return removed, nil
}
