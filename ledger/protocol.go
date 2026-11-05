// Package ledger implements a single-node, proof-of-work account ledger.
package ledger

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
)

const (
	NetworkID              = "go-blockchain-v1"
	Version         uint64 = 1
	Reward          uint64 = 50
	MaxTransactions        = 100
	Difficulty             = 16
)

var (
	ErrInvalid   = errors.New("invalid ledger data")
	ErrSignature = errors.New("invalid signature")
	ErrNetwork   = errors.New("wrong network")
	ErrNonce     = errors.New("invalid nonce")
	ErrFunds     = errors.New("insufficient funds")
	ErrOverflow  = errors.New("integer overflow")
	ErrDuplicate = errors.New("duplicate transaction")
	ErrCorrupt   = errors.New("corrupt ledger")
	ErrVersion   = errors.New("unsupported storage version")
	ErrConflict  = errors.New("mining snapshot changed")
)

type Hash [32]byte
type Address [32]byte

func (h Hash) String() string    { return hex.EncodeToString(h[:]) }
func (a Address) String() string { return hex.EncodeToString(a[:]) }
func ParseAddress(s string) (Address, error) {
	var a Address
	b, e := hex.DecodeString(s)
	if e != nil || len(b) != len(a) {
		return a, fmt.Errorf("address must be 64 hexadecimal characters")
	}
	copy(a[:], b)
	return a, nil
}

type Transaction struct {
	NetworkID string
	Sender    Address
	Recipient Address
	Amount    uint64
	Nonce     uint64
	Signature [64]byte
}
type Account struct {
	Balance uint64
	Nonce   uint64
}

// Encoding uses big-endian uint64 integers and length-prefixed strings.
func number(b *bytes.Buffer, n uint64) {
	var v [8]byte
	binary.BigEndian.PutUint64(v[:], n)
	b.Write(v[:])
}
func field(b *bytes.Buffer, s string) { number(b, uint64(len(s))); b.WriteString(s) }
func (t Transaction) SigningBytes() ([]byte, error) {
	if t.NetworkID != NetworkID {
		return nil, ErrNetwork
	}
	var b bytes.Buffer
	field(&b, "ledger/transaction/sign/v1")
	field(&b, t.NetworkID)
	b.Write(t.Sender[:])
	b.Write(t.Recipient[:])
	number(&b, t.Amount)
	number(&b, t.Nonce)
	return b.Bytes(), nil
}
func (t Transaction) ID() (Hash, error) {
	data, e := t.SigningBytes()
	if e != nil {
		return Hash{}, e
	}
	var b bytes.Buffer
	field(&b, "ledger/transaction/id/v1")
	b.Write(data)
	b.Write(t.Signature[:])
	return sha256.Sum256(b.Bytes()), nil
}
func (t Transaction) Verify() error {
	data, e := t.SigningBytes()
	if e != nil {
		return e
	}
	if t.Amount == 0 {
		return fmt.Errorf("%w: zero amount", ErrInvalid)
	}
	if !ed25519.Verify(t.Sender[:], data, t.Signature[:]) {
		return ErrSignature
	}
	return nil
}

type Block struct {
	Version      uint64
	NetworkID    string
	Height       uint64
	Timestamp    uint64
	Previous     Hash
	Transactions []Transaction
	RewardTo     Address
	Reward       uint64
	Difficulty   uint64
	Nonce        uint64
}

func Genesis() Block { return Block{Version: Version, NetworkID: NetworkID, Difficulty: Difficulty} }
func (b Block) encoding() ([]byte, error) {
	var out bytes.Buffer
	field(&out, "ledger/block/v1")
	number(&out, b.Version)
	// Bind the fixed issuance and transaction-limit rules into every block, including genesis.
	number(&out, Reward)
	number(&out, MaxTransactions)
	field(&out, b.NetworkID)
	number(&out, b.Height)
	number(&out, b.Timestamp)
	out.Write(b.Previous[:])
	number(&out, uint64(len(b.Transactions)))
	for _, t := range b.Transactions {
		id, e := t.ID()
		if e != nil {
			return nil, e
		}
		out.Write(id[:])
	}
	out.Write(b.RewardTo[:])
	number(&out, b.Reward)
	number(&out, b.Difficulty)
	number(&out, b.Nonce)
	return out.Bytes(), nil
}
func (b Block) Hash() (Hash, error) {
	data, err := b.encoding()
	if err != nil {
		return Hash{}, err
	}
	return sha256.Sum256(data), nil
}
func validWork(h Hash) bool { return h[0] == 0 && h[1] == 0 }
func mine(ctx context.Context, b Block) (Block, error) {
	data, err := b.encoding()
	if err != nil {
		return Block{}, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return Block{}, err
		}
		binary.BigEndian.PutUint64(data[len(data)-8:], b.Nonce)
		if validWork(sha256.Sum256(data)) {
			return b, nil
		}
		if b.Nonce == math.MaxUint64 {
			return Block{}, ErrOverflow
		}
		b.Nonce++
	}
}
func apply(accounts map[Address]Account, t Transaction) error {
	if e := t.Verify(); e != nil {
		return e
	}
	from := accounts[t.Sender]
	if from.Nonce == math.MaxUint64 {
		return ErrOverflow
	}
	if t.Nonce != from.Nonce+1 {
		return ErrNonce
	}
	if from.Balance < t.Amount {
		return ErrFunds
	}
	if t.Sender == t.Recipient {
		from.Nonce++
		accounts[t.Sender] = from
		return nil
	}
	to := accounts[t.Recipient]
	if to.Balance > math.MaxUint64-t.Amount {
		return ErrOverflow
	}
	from.Balance -= t.Amount
	from.Nonce++
	to.Balance += t.Amount
	accounts[t.Sender] = from
	accounts[t.Recipient] = to
	return nil
}
func applyBlock(accounts map[Address]Account, parent, b Block) error {
	return applyBlockContext(context.Background(), accounts, parent, b)
}
func applyBlockContext(ctx context.Context, accounts map[Address]Account, parent, b Block) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if parent.Height == math.MaxUint64 {
		return ErrOverflow
	}
	prev, e := parent.Hash()
	if e != nil {
		return e
	}
	if b.Version != Version || b.NetworkID != NetworkID || b.Height != parent.Height+1 || b.Previous != prev || b.Timestamp < parent.Timestamp || b.Difficulty != Difficulty || b.Reward != Reward || len(b.Transactions) > MaxTransactions {
		return ErrInvalid
	}
	h, e := b.Hash()
	if e != nil {
		return e
	}
	if !validWork(h) {
		return fmt.Errorf("%w: proof of work", ErrInvalid)
	}
	// Validate against a private candidate state so any later failure preserves
	// the caller's state as well as the persisted ledger.
	candidate := make(map[Address]Account, 2*len(b.Transactions)+1)
	candidate[b.RewardTo] = accounts[b.RewardTo]
	for _, transfer := range b.Transactions {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidate[transfer.Sender] = accounts[transfer.Sender]
		candidate[transfer.Recipient] = accounts[transfer.Recipient]
	}
	for _, t := range b.Transactions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e := apply(candidate, t); e != nil {
			return e
		}
	}
	a := candidate[b.RewardTo]
	if a.Balance > math.MaxUint64-Reward {
		return ErrOverflow
	}
	a.Balance += Reward
	candidate[b.RewardTo] = a
	if err := ctx.Err(); err != nil {
		return err
	}
	for address, account := range candidate {
		accounts[address] = account
	}
	return nil
}
