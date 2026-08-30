// Package processor coordinates one atomic processing attempt.
package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

var (
	ErrCommitRejected = errors.New("processing commit rejected")
	ErrCommitUnknown  = errors.New("processing commit outcome unknown")
)

type commitState uint8

const (
	commitConfirmed commitState = iota + 1
	commitRejected
	commitUnknown
)

// outcomeWriter deliberately omits transaction lifecycle methods. Domain work
// can add outcomes to the coordinator-owned transaction but cannot commit it.
type outcomeWriter interface {
	writeParserOutcome(context.Context, core.ParserTerminalOutcome) error
	writeDetectionContribution(context.Context, core.DetectionContribution) (bool, error)
	writeAlert(context.Context, core.Alert) error
	writeDecision(context.Context, core.Decision) error
	writeProjection(context.Context, core.DesiredBanProjection, time.Time) error
	writeCriticalAudit(context.Context, store.CriticalAudit) error
}

type processingUnitOfWork interface {
	outcomeWriter
	writeReceipt(context.Context, core.ProcessingReceipt) error
	commit(context.Context) (commitState, error)
	rollback() error
}

type processingStore interface {
	findReceipt(context.Context, core.DeliveryID) (core.ProcessingReceipt, bool, error)
	beginProcessing(context.Context) (processingUnitOfWork, error)
}

type attemptRunner interface {
	run(context.Context, outcomeWriter, core.Delivery) error
}

// Coordinator is the sole owner of begin, commit and rollback for a processing
// attempt. A successful return proves ProcessingComplete and SourceDurable.
type Coordinator struct {
	store           processingStore
	runner          attemptRunner
	clock           func() time.Time
	readbackTimeout time.Duration
	flightMu        sync.Mutex
	flights         map[core.DeliveryID]chan struct{}
}

func NewCoordinator(store processingStore, runner attemptRunner) *Coordinator {
	return &Coordinator{
		store:           store,
		runner:          runner,
		clock:           func() time.Time { return time.Now().UTC() },
		readbackTimeout: time.Second,
		flights:         make(map[core.DeliveryID]chan struct{}),
	}
}

func (c *Coordinator) Process(ctx context.Context, delivery core.Delivery) (core.DurableCompletion, error) {
	if err := delivery.Validate(); err != nil {
		return core.DurableCompletion{}, fmt.Errorf("validate delivery: %w", err)
	}
	for {
		done, leader := c.acquireFlight(delivery.ID)
		if leader {
			completion, err := c.processLeader(ctx, delivery)
			c.releaseFlight(delivery.ID, done)
			return completion, err
		}
		select {
		case <-done:
			// Re-check the durable receipt. If the leader failed before commit,
			// this caller becomes the next serialized attempt.
		case <-ctx.Done():
			return core.DurableCompletion{}, ctx.Err()
		}
	}
}

func (c *Coordinator) processLeader(ctx context.Context, delivery core.Delivery) (core.DurableCompletion, error) {
	receipt, found, err := c.store.findReceipt(ctx, delivery.ID)
	if err != nil {
		return core.DurableCompletion{}, fmt.Errorf("find processing receipt: %w", err)
	}
	if found {
		return completionFromReceipt(delivery, receipt)
	}

	tx, err := c.store.beginProcessing(ctx)
	if err != nil {
		return core.DurableCompletion{}, fmt.Errorf("begin processing: %w", err)
	}
	if err := c.runner.run(ctx, tx, delivery); err != nil {
		return core.DurableCompletion{}, rollbackWithCause(tx, fmt.Errorf("run processing attempt: %w", err))
	}

	receipt = core.ProcessingReceipt{
		DeliveryID: delivery.ID,
		SourceID:   delivery.Record.SourceID,
		Position:   delivery.Record.Position,
		Kind:       core.ReceiptSuccess,
		Committed:  c.clock(),
	}
	if err := tx.writeReceipt(ctx, receipt); err != nil {
		return core.DurableCompletion{}, rollbackWithCause(tx, fmt.Errorf("write processing receipt: %w", err))
	}

	state, commitErr := tx.commit(ctx)
	switch state {
	case commitConfirmed:
		if commitErr != nil {
			return core.DurableCompletion{}, fmt.Errorf("commit reported confirmed with error: %w", commitErr)
		}
		return completionFromReceipt(delivery, receipt)
	case commitRejected:
		cause := ErrCommitRejected
		if commitErr != nil {
			cause = errors.Join(cause, commitErr)
		}
		// Rejected is a terminal transaction outcome: the UnitOfWork must have
		// already rolled back before reporting it, so a second rollback is wrong.
		return core.DurableCompletion{}, cause
	case commitUnknown:
		readbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.readbackTimeout)
		defer cancel()
		persisted, persistedFound, readErr := c.store.findReceipt(readbackContext, delivery.ID)
		if readErr == nil && persistedFound {
			return completionFromReceipt(delivery, persisted)
		}
		return core.DurableCompletion{}, errors.Join(ErrCommitUnknown, commitErr, readErr)
	default:
		cause := fmt.Errorf("unsupported commit state %d", state)
		if commitErr != nil {
			cause = errors.Join(cause, commitErr)
		}
		return core.DurableCompletion{}, rollbackWithCause(tx, cause)
	}
}

func (c *Coordinator) acquireFlight(id core.DeliveryID) (chan struct{}, bool) {
	c.flightMu.Lock()
	defer c.flightMu.Unlock()
	if done, found := c.flights[id]; found {
		return done, false
	}
	done := make(chan struct{})
	c.flights[id] = done
	return done, true
}

func (c *Coordinator) releaseFlight(id core.DeliveryID, done chan struct{}) {
	c.flightMu.Lock()
	delete(c.flights, id)
	close(done)
	c.flightMu.Unlock()
}

func completionFromReceipt(delivery core.Delivery, receipt core.ProcessingReceipt) (core.DurableCompletion, error) {
	if err := receipt.Validate(); err != nil {
		return core.DurableCompletion{}, fmt.Errorf("validate receipt: %w", err)
	}
	if receipt.DeliveryID != delivery.ID {
		return core.DurableCompletion{}, fmt.Errorf("receipt delivery id mismatch")
	}
	if receipt.SourceID != delivery.Record.SourceID {
		return core.DurableCompletion{}, fmt.Errorf("receipt source id mismatch")
	}
	if receipt.Position != delivery.Record.Position {
		return core.DurableCompletion{}, fmt.Errorf("receipt source position mismatch")
	}
	if receipt.Kind != core.ReceiptSuccess && receipt.Kind != core.ReceiptRecordPermanent {
		return core.DurableCompletion{}, fmt.Errorf("unsupported receipt kind %d", receipt.Kind)
	}
	return core.DurableCompletion{
		SourceID:   delivery.Record.SourceID,
		DeliveryID: delivery.ID,
		Sequence:   delivery.Sequence,
		Position:   delivery.Record.Position,
	}, nil
}

func rollbackWithCause(tx processingUnitOfWork, cause error) error {
	if err := tx.rollback(); err != nil {
		return errors.Join(cause, fmt.Errorf("rollback processing: %w", err))
	}
	return cause
}
