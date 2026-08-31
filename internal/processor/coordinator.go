// Package processor coordinates one atomic processing attempt.
package processor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/decision"
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
	writeDetectionOutcome(context.Context, core.DetectionTerminalOutcome) error
	writeDetectionContribution(context.Context, core.DetectionContribution) (bool, error)
	writeAlert(context.Context, core.Alert) error
	recordAutomaticDecision(context.Context, decision.AutomaticRequest) error
	writeCriticalAudit(context.Context, store.CriticalAudit) error
}

type processingUnitOfWork interface {
	outcomeWriter
	finalizeDesiredState(context.Context) error
	writeReceipt(context.Context, core.ProcessingReceipt) error
	commit(context.Context) (commitState, error)
	notifyCommitted(context.Context) error
	rollback() error
}

type processingStore interface {
	findReceipt(context.Context, core.DeliveryID) (core.ProcessingReceipt, bool, error)
	notifyReceiptReplay(context.Context) error
	beginProcessing(context.Context) (processingUnitOfWork, error)
}

// preparedAttempt contains only already-computed semantic writes. Parser and
// Detection evaluation happen before the Coordinator opens the SQLite unit of
// work, keeping the transaction free of external or expensive computation.
type preparedAttempt interface {
	write(context.Context, outcomeWriter) error
	terminal() (core.ReceiptKind, *core.PermanentFailure)
	confirm()
	abort()
}

type attemptRunner interface {
	prepare(context.Context, core.Delivery) (preparedAttempt, error)
}

type deferredPreparedAttempt interface {
	deferResolution()
}

type pendingAttemptResolver interface {
	resolvePending(core.DeliveryID, bool)
}

type sharedAttemptGate interface {
	acquireDelivery(context.Context, core.DeliveryID) (func(), error)
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
	pendingMu       sync.Mutex
	pending         map[core.DeliveryID]preparedAttempt
}

func NewCoordinator(store processingStore, runner attemptRunner) *Coordinator {
	return &Coordinator{
		store:           store,
		runner:          runner,
		clock:           func() time.Time { return time.Now().UTC() },
		readbackTimeout: time.Second,
		flights:         make(map[core.DeliveryID]chan struct{}),
		pending:         make(map[core.DeliveryID]preparedAttempt),
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
	if gate, ok := c.runner.(sharedAttemptGate); ok {
		release, err := gate.acquireDelivery(ctx, delivery.ID)
		if err != nil {
			return core.DurableCompletion{}, fmt.Errorf("acquire shared processing attempt: %w", err)
		}
		defer release()
	}
	receipt, found, err := c.store.findReceipt(ctx, delivery.ID)
	if err != nil {
		return core.DurableCompletion{}, fmt.Errorf("find processing receipt: %w", err)
	}
	if found {
		completion, err := completionFromReceipt(delivery, receipt)
		if err != nil {
			return core.DurableCompletion{}, err
		}
		c.resolvePending(delivery.ID, true)
		if err := c.store.notifyReceiptReplay(ctx); err != nil {
			return completion, err
		}
		return completion, nil
	}
	c.resolvePending(delivery.ID, false)

	prepared, err := c.runner.prepare(ctx, delivery)
	if err != nil {
		return core.DurableCompletion{}, fmt.Errorf("prepare processing attempt: %w", err)
	}
	if prepared == nil {
		return core.DurableCompletion{}, fmt.Errorf("prepare processing attempt: nil result")
	}

	tx, err := c.store.beginProcessing(ctx)
	if err != nil {
		prepared.abort()
		return core.DurableCompletion{}, fmt.Errorf("begin processing: %w", err)
	}
	if err := prepared.write(ctx, tx); err != nil {
		prepared.abort()
		return core.DurableCompletion{}, rollbackWithCause(tx, fmt.Errorf("run processing attempt: %w", err))
	}
	if err := tx.finalizeDesiredState(ctx); err != nil {
		prepared.abort()
		return core.DurableCompletion{}, rollbackWithCause(tx, fmt.Errorf("finalize desired state: %w", err))
	}

	kind, failure := prepared.terminal()
	receipt = core.ProcessingReceipt{
		DeliveryID: delivery.ID,
		SourceID:   delivery.Record.SourceID,
		Position:   delivery.Record.Position,
		Kind:       kind,
		Failure:    clonePermanentFailure(failure),
		Committed:  c.clock(),
	}
	if err := tx.writeReceipt(ctx, receipt); err != nil {
		prepared.abort()
		return core.DurableCompletion{}, rollbackWithCause(tx, fmt.Errorf("write processing receipt: %w", err))
	}

	state, commitErr := tx.commit(ctx)
	switch state {
	case commitConfirmed:
		if commitErr != nil {
			// Confirmed means durable state won. Keep the post-commit ledger in
			// sync even if an adapter also reports an invariant error.
			prepared.confirm()
			notifyErr := tx.notifyCommitted(ctx)
			return core.DurableCompletion{}, errors.Join(
				fmt.Errorf("commit reported confirmed with error: %w", commitErr), notifyErr,
			)
		}
		completion, err := completionFromReceipt(delivery, receipt)
		if err != nil {
			// The transaction is already durable. Preserve Window/DB alignment
			// even when an adapter accepted an invalid locally-built receipt.
			prepared.confirm()
			return core.DurableCompletion{}, err
		}
		prepared.confirm()
		if err := tx.notifyCommitted(ctx); err != nil {
			return completion, err
		}
		return completion, nil
	case commitRejected:
		prepared.abort()
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
			completion, err := completionFromReceipt(delivery, persisted)
			if err != nil {
				// A row exists at the durability key, but it cannot prove this
				// completion. Keep the reservation unresolved until a later
				// read can prove committed or absent state.
				c.rememberPending(delivery.ID, prepared)
				return core.DurableCompletion{}, err
			}
			prepared.confirm()
			if err := tx.notifyCommitted(readbackContext); err != nil {
				return completion, err
			}
			return completion, nil
		}
		if readErr == nil {
			prepared.abort()
		} else {
			c.rememberPending(delivery.ID, prepared)
		}
		return core.DurableCompletion{}, errors.Join(ErrCommitUnknown, commitErr, readErr)
	default:
		prepared.abort()
		cause := fmt.Errorf("unsupported commit state %d", state)
		if commitErr != nil {
			cause = errors.Join(cause, commitErr)
		}
		return core.DurableCompletion{}, rollbackWithCause(tx, cause)
	}
}

func (c *Coordinator) rememberPending(id core.DeliveryID, prepared preparedAttempt) {
	if deferred, ok := prepared.(deferredPreparedAttempt); ok {
		deferred.deferResolution()
	}
	c.pendingMu.Lock()
	previous := c.pending[id]
	c.pending[id] = prepared
	c.pendingMu.Unlock()
	if previous != nil {
		previous.abort()
	}
}

func (c *Coordinator) resolvePending(id core.DeliveryID, committed bool) {
	c.pendingMu.Lock()
	prepared := c.pending[id]
	delete(c.pending, id)
	c.pendingMu.Unlock()
	if prepared != nil {
		if committed {
			prepared.confirm()
		} else {
			prepared.abort()
		}
	}
	if resolver, ok := c.runner.(pendingAttemptResolver); ok {
		resolver.resolvePending(id, committed)
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

func clonePermanentFailure(failure *core.PermanentFailure) *core.PermanentFailure {
	if failure == nil {
		return nil
	}
	cloned := *failure
	return &cloned
}
