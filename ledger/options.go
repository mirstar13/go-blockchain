package ledger

import (
	"errors"
	"fmt"
)

const DefaultMaxPending = 1000
const DefaultMaxPendingPerSender = 100

// Options controls local admission policy, not consensus or stored data.
type Options struct {
	MaxPending          int
	MaxPendingPerSender int
}

func (o Options) normalized() (Options, error) {
	if o.MaxPending == 0 {
		o.MaxPending = DefaultMaxPending
	}
	if o.MaxPendingPerSender == 0 {
		o.MaxPendingPerSender = DefaultMaxPendingPerSender
	}
	if o.MaxPending < 0 || o.MaxPendingPerSender < 0 || o.MaxPendingPerSender > o.MaxPending {
		return o, errors.New("queue limits must be positive and per-sender limit must not exceed global limit")
	}
	return o, nil
}

var ErrQueueFull = errors.New("pending queue is full")

// QueueFullError reports the count before the rejected admission. Scope is
// "global" or "sender"; errors.Is(err, ErrQueueFull) identifies either case.
type QueueFullError struct {
	Scope string
	Limit int
	Count int
}

func (e *QueueFullError) Error() string {
	return fmt.Sprintf("%s: %s limit %d, current count %d", ErrQueueFull, e.Scope, e.Limit, e.Count)
}
func (e *QueueFullError) Unwrap() error { return ErrQueueFull }
