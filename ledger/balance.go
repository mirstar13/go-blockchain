package ledger

import bolt "go.etcd.io/bbolt"

// BalanceSummary reports confirmed funds and reservations from one database view.
// Pending incoming transfers do not increase Available.
type BalanceSummary struct {
	Confirmed uint64
	Reserved  uint64
	Available uint64
	Nonce     uint64
}

func (c *Chain) BalanceSummary(address Address) (BalanceSummary, error) {
	var result BalanceSummary
	err := c.db.View(func(tx *bolt.Tx) error {
		state, err := readAccounts(tx)
		if err != nil {
			return err
		}
		confirmed := state[address]
		pending, err := readPending(tx)
		if err != nil {
			return err
		}
		if err = reserve(state, pending); err != nil {
			return err
		}
		result = BalanceSummary{Confirmed: confirmed.Balance, Reserved: confirmed.Balance - state[address].Balance, Available: state[address].Balance, Nonce: confirmed.Nonce}
		return nil
	})
	return result, err
}
