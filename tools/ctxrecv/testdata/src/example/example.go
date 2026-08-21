package example

import (
	"context"
	"time"

	"base"
)

type migCtx struct {
	ctx        context.Context
	PanicAbort chan error
}

func (m *migCtx) GetContext() context.Context { return m.ctx }

type Migrator struct {
	migrationContext *migCtx
	guardedOnly      chan bool     // sent to only via SendWithContext
	alsoBareSend     chan bool     // has a bare send too
	closedChan       chan struct{} // closed, never sent
	queue            chan int      // sent to only via SendWithContext
}

func (m *Migrator) produce() {
	ctx := m.migrationContext.GetContext()
	_ = base.SendWithContext(ctx, m.guardedOnly, true)
	_ = base.SendWithContext(ctx, m.queue, 1)
	_ = base.SendWithContext(ctx, m.migrationContext.PanicAbort, nil)

	_ = base.SendWithContext(ctx, m.alsoBareSend, true)
	m.alsoBareSend <- true // a bare send exempts this channel

	close(m.closedChan)
}

func (m *Migrator) bare() {
	<-m.guardedOnly // want `blocking receive from guardedOnly`
}

func (m *Migrator) bareAssign() bool {
	v := <-m.guardedOnly // want `blocking receive from guardedOnly`
	return v
}

// A nested field selector must resolve to the field, not the intermediate.
func (m *Migrator) nestedSelector() {
	err := <-m.migrationContext.PanicAbort // want `blocking receive from PanicAbort`
	_ = err
}

// A select with no escape arm is still unwakeable.
func (m *Migrator) selectNoEscape() {
	select {
	case <-m.guardedOnly: // want `blocking receive from guardedOnly`
	case <-m.queue: // want `blocking receive from queue`
	}
}

// A receive in a case *body* is an ordinary blocking receive, not part of the
// select's communication.
func (m *Migrator) inCaseBody() {
	select {
	case <-m.migrationContext.GetContext().Done():
		<-m.guardedOnly // want `blocking receive from guardedOnly`
	}
}

func (m *Migrator) okDone() {
	select {
	case <-m.guardedOnly:
	case <-m.migrationContext.GetContext().Done():
	}
}

func (m *Migrator) okDefault() {
	select {
	case <-m.guardedOnly:
	default:
	}
}

func (m *Migrator) okTimeAfter() {
	select {
	case <-m.guardedOnly:
	case <-time.After(time.Second):
	}
}

func (m *Migrator) okTicker(ticker *time.Ticker) {
	select {
	case <-m.queue:
	case <-ticker.C:
	}
}

// Exempt: a bare sender does not abandon on cancellation.
func (m *Migrator) okBareSendChannel() {
	<-m.alsoBareSend
}

// Exempt: closing wakes every receiver.
func (m *Migrator) okClosed() {
	<-m.closedChan
}

// Exempt: not a struct field.
func okLocal() {
	ch := make(chan bool, 1)
	ch <- true
	<-ch
}
