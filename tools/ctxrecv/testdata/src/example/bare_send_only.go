package example

// A channel with no SendWithContext send at all is out of scope entirely: the
// invariant only applies once the sole sender is known to abandon.
type Throttler struct {
	firstCollected chan bool
}

func (t *Throttler) produce() {
	t.firstCollected <- true
}

func (t *Throttler) consume() {
	<-t.firstCollected
	<-t.firstCollected
}
