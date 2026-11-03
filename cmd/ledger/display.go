package main

import (
	"encoding/hex"
	"github.com/mirstar13/go-blockchain/ledger"
)

// Display types deliberately keep presentation changes out of stored JSON and
// canonical signing/hashing formats.
type transactionDisplay struct {
	ID        string
	NetworkID string
	Sender    string
	Recipient string
	Amount    uint64
	Nonce     uint64
	Signature string
}

func displayTransactions(transactions []ledger.Transaction) ([]transactionDisplay, error) {
	out := make([]transactionDisplay, 0, len(transactions))
	for _, tx := range transactions {
		id, err := tx.ID()
		if err != nil {
			return nil, err
		}
		out = append(out, transactionDisplay{ID: id.String(), NetworkID: tx.NetworkID, Sender: tx.Sender.String(), Recipient: tx.Recipient.String(), Amount: tx.Amount, Nonce: tx.Nonce, Signature: hex.EncodeToString(tx.Signature[:])})
	}
	return out, nil
}

type blockDisplay struct {
	Hash         string
	Version      uint64
	NetworkID    string
	Height       uint64
	Timestamp    uint64
	Previous     string
	Transactions []transactionDisplay
	RewardTo     string
	Reward       uint64
	Difficulty   uint64
	Nonce        uint64
}

func displayBlocks(blocks []ledger.Block) ([]blockDisplay, error) {
	out := make([]blockDisplay, 0, len(blocks))
	for _, block := range blocks {
		hash, err := block.Hash()
		if err != nil {
			return nil, err
		}
		transactions, err := displayTransactions(block.Transactions)
		if err != nil {
			return nil, err
		}
		out = append(out, blockDisplay{Hash: hash.String(), Version: block.Version, NetworkID: block.NetworkID, Height: block.Height, Timestamp: block.Timestamp, Previous: block.Previous.String(), Transactions: transactions, RewardTo: block.RewardTo.String(), Reward: block.Reward, Difficulty: block.Difficulty, Nonce: block.Nonce})
	}
	return out, nil
}
