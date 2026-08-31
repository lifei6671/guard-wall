package decision

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

const expirationDetectionInterval = 60 * time.Second

type pendingTargetEnforcementChangeReader interface {
	PendingTargetEnforcementChanges(context.Context) ([]TargetEnforcementChange, error)
}

// RunExpirationScheduler expires due Decisions immediately at startup, then
// repeats from each sweep's absolute 60-second deadline until ctx is canceled.
// A startup sweep also redelivers durable Target work left pending by a prior
// post-commit wake failure.
func (s *LifecycleService) RunExpirationScheduler(ctx context.Context) error {
	if err := s.validateExpirationScheduler(ctx); err != nil {
		return err
	}
	pending, ok := s.runner.(pendingTargetEnforcementChangeReader)
	if !ok {
		return fmt.Errorf("decision transaction runner cannot read pending target enforcement changes")
	}

	firstSweep := true
	for {
		startedAt := s.expirationClock.Now()
		result, err := s.Expire(ctx, startedAt)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("expire due decisions: %w", err)
		}
		if firstSweep {
			changes, err := pending.PendingTargetEnforcementChanges(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("read pending target enforcement changes: %w", err)
			}
			changes = excludeCommittedTargetChanges(changes, result.EnforcementChanges)
			if err := WakeCommittedTargets(ctx, s.wake, changes); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("redeliver pending target enforcement changes: %w", err)
			}
			firstSweep = false
		}

		delay := startedAt.Add(expirationDetectionInterval).Sub(s.expirationClock.Now())
		if delay <= 0 {
			continue
		}
		timer := s.expirationClock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C():
			timer.Stop()
		}
	}
}

// PrepareExpirationStartup expires all Decisions due at startup without
// sending wakeups. A Dispatcher started after this method observes the durable
// Pending Target state through its startup recovery scan, avoiding queue
// backpressure before the worker owns the queue.
func (s *LifecycleService) PrepareExpirationStartup(ctx context.Context) (time.Time, error) {
	if err := s.validateExpirationScheduler(ctx); err != nil {
		return time.Time{}, err
	}
	startedAt := s.expirationClock.Now()
	if _, err := s.expire(ctx, startedAt, false); err != nil {
		if ctx.Err() != nil {
			return startedAt, nil
		}
		return time.Time{}, fmt.Errorf("prepare startup expiration: %w", err)
	}
	return startedAt, nil
}

// RunExpirationSchedulerAfterStartup waits one absolute 60-second interval
// before the first sweep. It is paired with PrepareExpirationStartup by the
// runtime owner so startup recovery is ordered before Dispatcher mutation.
func (s *LifecycleService) RunExpirationSchedulerAfterStartup(
	ctx context.Context,
	startupSweepStartedAt time.Time,
) error {
	if err := s.validateExpirationScheduler(ctx); err != nil {
		return err
	}
	if startupSweepStartedAt.IsZero() {
		return fmt.Errorf("startup expiration sweep time is required")
	}
	startedAt := startupSweepStartedAt
	for {
		if !s.waitForExpirationDeadline(ctx, startedAt) {
			return nil
		}
		startedAt = s.expirationClock.Now()
		if _, err := s.Expire(ctx, startedAt); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("expire due decisions: %w", err)
		}
	}
}

func (s *LifecycleService) validateExpirationScheduler(ctx context.Context) error {
	if s == nil || s.runner == nil || s.finalizer == nil || s.wake == nil || s.expirationClock == nil {
		return fmt.Errorf("decision expiration scheduler is not initialized")
	}
	if ctx == nil {
		return fmt.Errorf("expiration scheduler context is required")
	}
	return nil
}

func (s *LifecycleService) waitForExpirationDeadline(ctx context.Context, startedAt time.Time) bool {
	delay := startedAt.Add(expirationDetectionInterval).Sub(s.expirationClock.Now())
	if delay <= 0 {
		return true
	}
	timer := s.expirationClock.NewTimer(delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false
	case <-timer.C():
		timer.Stop()
		return true
	}
}

func excludeCommittedTargetChanges(
	pending []TargetEnforcementChange,
	committed []TargetEnforcementChange,
) []TargetEnforcementChange {
	if len(pending) == 0 || len(committed) == 0 {
		return pending
	}
	type changeKey struct {
		nodeID     core.NodeID
		target     netip.Prefix
		generation core.TargetEnforcementGeneration
	}
	current := make(map[changeKey]struct{}, len(committed))
	for _, change := range committed {
		current[changeKey{
			nodeID: change.NodeID, target: change.Target, generation: change.Generation,
		}] = struct{}{}
	}
	filtered := make([]TargetEnforcementChange, 0, len(pending))
	for _, change := range pending {
		key := changeKey{
			nodeID: change.NodeID, target: change.Target, generation: change.Generation,
		}
		if _, duplicate := current[key]; !duplicate {
			filtered = append(filtered, change)
		}
	}
	return filtered
}
