package base

import "context"

// SendWithContext mirrors the real base.SendWithContext: it abandons the send
// once the context is cancelled, which is what makes an unguarded receive on
// the other end unwakeable.
func SendWithContext[T any](ctx context.Context, ch chan<- T, val T) error {
	select {
	case ch <- val:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
