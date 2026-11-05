package ledger

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestIndependentProtocolVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/protocol_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Seed, Sender, Recipient string
		Amount, Nonce, Sequence uint64
		SigningBytes            string `json:"signing_bytes"`
		Signature               string
		TransactionID           string `json:"transaction_id"`
		CancellationBytes       string `json:"cancellation_bytes"`
		CancellationSignature   string `json:"cancellation_signature"`
		Previous                string
		BlockHash               string `json:"block_hash"`
	}
	if err = json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 {
		t.Fatal("missing vectors")
	}
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for _, v := range vectors {
		t.Run(v.Sender[:8], func(t *testing.T) {
			var tx Transaction
			tx.NetworkID = NetworkID
			tx.Amount = v.Amount
			tx.Nonce = v.Nonce
			copy(tx.Sender[:], decode(v.Sender))
			copy(tx.Recipient[:], decode(v.Recipient))
			copy(tx.Signature[:], decode(v.Signature))
			signing, err := tx.SigningBytes()
			if err != nil || !bytes.Equal(signing, decode(v.SigningBytes)) {
				t.Fatal("transaction encoding drift", err)
			}
			private := ed25519.NewKeyFromSeed(decode(v.Seed))
			if !bytes.Equal(ed25519.Sign(private, signing), decode(v.Signature)) {
				t.Fatal("signature drift")
			}
			if err = tx.Verify(); err != nil {
				t.Fatal(err)
			}
			id, err := tx.ID()
			if err != nil || id.String() != v.TransactionID {
				t.Fatal("transaction ID drift", id, err)
			}
			request := Cancellation{ID: id, Sender: tx.Sender, Sequence: v.Sequence}
			if !bytes.Equal(request.SigningBytes(), decode(v.CancellationBytes)) {
				t.Fatal("cancellation encoding drift")
			}
			if !bytes.Equal(ed25519.Sign(private, request.SigningBytes()), decode(v.CancellationSignature)) {
				t.Fatal("cancellation signature drift")
			}
			if ed25519.Verify(tx.Sender[:], request.SigningBytes(), tx.Signature[:]) {
				t.Fatal("domain separation broken")
			}
			var previous Hash
			copy(previous[:], decode(v.Previous))
			block := Block{Version: Version, NetworkID: NetworkID, Height: 7, Timestamp: 1700000000, Previous: previous, Transactions: []Transaction{tx}, RewardTo: tx.Recipient, Reward: Reward, Difficulty: Difficulty, Nonce: 123456789}
			hash, err := block.Hash()
			if err != nil || hash.String() != v.BlockHash {
				t.Fatal("block encoding drift", hash, err)
			}
			mutations := []func(*Transaction){func(x *Transaction) { x.Sender[0] ^= 1 }, func(x *Transaction) { x.Recipient[0] ^= 1 }, func(x *Transaction) { x.Amount ^= 1 }, func(x *Transaction) { x.Nonce ^= 1 }, func(x *Transaction) { x.Signature[0] ^= 1 }, func(x *Transaction) { x.NetworkID = "other" }}
			for _, mutate := range mutations {
				changed := tx
				mutate(&changed)
				if changed.Verify() == nil {
					t.Fatal("signed field mutation accepted")
				}
			}
		})
	}
}
