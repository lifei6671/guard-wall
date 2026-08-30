// Package detection contains the in-memory M0 detection primitives.
package detection

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lifei6671/guard-wall/internal/core"
)

// ContributionKey is the frozen idempotency identity of one Event/Rule match.
type ContributionKey struct {
	EventID     core.EventID
	RuleID      core.RuleID
	RuleVersion core.RuleVersion
}

// WindowKey identifies one in-memory Rule/Group window.
type WindowKey struct {
	RuleID      core.RuleID
	RuleVersion core.RuleVersion
	GroupKey    string
}

// Candidate is one proposed membership mutation. PrepareBatch only stages the
// mutation; it does not make it visible through Snapshot.
type Candidate struct {
	DeliveryID  core.DeliveryID
	Key         ContributionKey
	Window      WindowKey
	DistinctKey string
	ObservedAt  time.Time
}

// Snapshot is the visible committed membership state of one Window.
type Snapshot struct {
	Count         uint64
	DistinctCount uint64
}

// Preview is the state that would be visible after committing all preceding
// candidates in the same batch, including this candidate when it is new.
type Preview struct {
	Candidate      Candidate
	Snapshot       Snapshot
	WillContribute bool
}

// Ledger is the M0 post-commit membership primitive. It intentionally does not
// implement sliding-window expiry, thresholds, group capacity, or distinct
// capacity; those remain M4 Detection Engine responsibilities.
type Ledger struct {
	mu          sync.Mutex
	windows     map[WindowKey]*windowSlot
	memberships map[ContributionKey]memberRecord
	claims      map[ContributionKey]*Reservation
	pending     map[core.DeliveryID]*Reservation
	deliveries  map[core.DeliveryID]*deliverySlot
}

type windowSlot struct {
	guard    chan struct{}
	count    uint64
	distinct map[string]uint64
}

type deliverySlot struct {
	guard chan struct{}
	refs  int
}

type memberRecord struct {
	window      WindowKey
	distinctKey string
	observedAt  time.Time
}

// Reservation holds all involved Window guards until Confirm or Abort. Exactly
// one of those operations wins; both are safe to call repeatedly.
type Reservation struct {
	once       sync.Once
	ledger     *Ledger
	deliveryID core.DeliveryID
	locked     []*windowSlot
	candidates []Candidate
}

// NewLedger constructs an empty in-memory membership ledger.
func NewLedger() *Ledger {
	return &Ledger{
		windows:     make(map[WindowKey]*windowSlot),
		memberships: make(map[ContributionKey]memberRecord),
		claims:      make(map[ContributionKey]*Reservation),
		pending:     make(map[core.DeliveryID]*Reservation),
		deliveries:  make(map[core.DeliveryID]*deliverySlot),
	}
}

// AcquireDelivery serializes the receipt pre-check and processing attempt for
// one Delivery across Coordinator instances sharing this Ledger.
func (l *Ledger) AcquireDelivery(ctx context.Context, deliveryID core.DeliveryID) (func(), error) {
	if l == nil || ctx == nil {
		return nil, fmt.Errorf("acquire delivery: Ledger and context are required")
	}
	if !core.ValidDeliveryID(deliveryID) {
		return nil, fmt.Errorf("acquire delivery: delivery id is not canonical")
	}
	l.mu.Lock()
	l.ensureMapsLocked()
	slot := l.deliveries[deliveryID]
	if slot == nil {
		slot = &deliverySlot{guard: make(chan struct{}, 1)}
		slot.guard <- struct{}{}
		l.deliveries[deliveryID] = slot
	}
	slot.refs++
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.releaseDeliveryReference(deliveryID, slot)
		return nil, ctx.Err()
	case <-slot.guard:
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			slot.guard <- struct{}{}
			l.releaseDeliveryReference(deliveryID, slot)
		})
	}, nil
}

// PrepareBatch locks all referenced Windows in canonical order and computes
// post-commit previews without changing visible state. The caller must promptly
// call Confirm after durable commit or Abort after a known rollback.
func (l *Ledger) PrepareBatch(
	ctx context.Context,
	deliveryID core.DeliveryID,
	candidates []Candidate,
) (*Reservation, []Preview, error) {
	if l == nil {
		return nil, nil, fmt.Errorf("detection ledger is required")
	}
	if ctx == nil {
		return nil, nil, fmt.Errorf("prepare window batch: context is required")
	}
	if !core.ValidDeliveryID(deliveryID) {
		return nil, nil, fmt.Errorf("prepare window batch: delivery id is not canonical")
	}
	for index, candidate := range candidates {
		if err := validateCandidate(deliveryID, candidate); err != nil {
			return nil, nil, fmt.Errorf("prepare window batch: candidate %d: %w", index, err)
		}
	}

	reservation := &Reservation{ledger: l, deliveryID: deliveryID}
	keys := uniqueSortedWindowKeys(candidates)
	locked := make([]*windowSlot, 0, len(keys))
	for _, key := range keys {
		slot := l.window(key)
		select {
		case <-ctx.Done():
			releaseSlots(locked)
			return nil, nil, ctx.Err()
		case <-slot.guard:
			locked = append(locked, slot)
		}
	}
	if err := ctx.Err(); err != nil {
		releaseSlots(locked)
		return nil, nil, err
	}

	reservation.locked = locked
	previews, staged, err := l.prepareLocked(reservation, candidates)
	if err != nil {
		releaseSlots(locked)
		return nil, nil, err
	}
	reservation.candidates = staged
	return reservation, previews, nil
}

// Snapshot returns only committed state. A pending Reservation for this Window
// blocks the read and remains cancellable through ctx.
func (l *Ledger) Snapshot(ctx context.Context, key WindowKey) (Snapshot, error) {
	if l == nil {
		return Snapshot{}, fmt.Errorf("detection ledger is required")
	}
	if ctx == nil {
		return Snapshot{}, fmt.Errorf("snapshot window: context is required")
	}
	if err := validateWindowKey(key); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot window: %w", err)
	}
	slot := l.window(key)
	select {
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-slot.guard:
	}
	defer releaseSlot(slot)
	return snapshotOf(slot), nil
}

// Confirm atomically applies every new membership and releases all Window
// guards. It is an in-memory, non-I/O operation intended for post-commit use.
func (r *Reservation) Confirm() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		l := r.ledger
		l.mu.Lock()
		for _, candidate := range r.candidates {
			slot := l.windows[candidate.Window]
			slot.count++
			slot.distinct[candidate.DistinctKey]++
			l.memberships[candidate.Key] = memberRecord{
				window:      candidate.Window,
				distinctKey: candidate.DistinctKey,
				observedAt:  candidate.ObservedAt,
			}
			delete(l.claims, candidate.Key)
		}
		if l.pending[r.deliveryID] == r {
			delete(l.pending, r.deliveryID)
		}
		l.mu.Unlock()
		releaseSlots(r.locked)
	})
}

// Abort discards all staged memberships and releases all Window guards.
func (r *Reservation) Abort() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		l := r.ledger
		l.mu.Lock()
		for _, candidate := range r.candidates {
			if l.claims[candidate.Key] == r {
				delete(l.claims, candidate.Key)
			}
		}
		if l.pending[r.deliveryID] == r {
			delete(l.pending, r.deliveryID)
		}
		l.mu.Unlock()
		releaseSlots(r.locked)
	})
}

// DeferResolution registers an ambiguous commit reservation with the shared
// Ledger. Window guards remain held until receipt readback proves committed or
// absent state, including after Coordinator reconstruction.
func (r *Reservation) DeferResolution() {
	if r == nil || r.ledger == nil {
		return
	}
	r.ledger.mu.Lock()
	r.ledger.ensureMapsLocked()
	if current := r.ledger.pending[r.deliveryID]; current == nil || current == r {
		r.ledger.pending[r.deliveryID] = r
	}
	r.ledger.mu.Unlock()
}

func (l *Ledger) releaseDeliveryReference(deliveryID core.DeliveryID, slot *deliverySlot) {
	l.mu.Lock()
	slot.refs--
	if slot.refs == 0 && l.deliveries[deliveryID] == slot {
		delete(l.deliveries, deliveryID)
	}
	l.mu.Unlock()
}

// ResolveDeferred applies or aborts an ambiguous reservation registered for a
// Delivery. It returns whether this Ledger owned such a reservation.
func (l *Ledger) ResolveDeferred(deliveryID core.DeliveryID, committed bool) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	reservation := l.pending[deliveryID]
	l.mu.Unlock()
	if reservation == nil {
		return false
	}
	if committed {
		reservation.Confirm()
	} else {
		reservation.Abort()
	}
	return true
}

func (l *Ledger) prepareLocked(reservation *Reservation, candidates []Candidate) ([]Preview, []Candidate, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := make(map[ContributionKey]Candidate)
	for _, candidate := range candidates {
		if prior, ok := seen[candidate.Key]; ok {
			if !sameCandidateMapping(prior, candidate) {
				return nil, nil, fmt.Errorf("membership %q has inconsistent candidates in one batch", candidate.Key.EventID)
			}
			continue
		}
		seen[candidate.Key] = candidate
		if member, ok := l.memberships[candidate.Key]; ok {
			if !sameMemberMapping(member, candidate) {
				return nil, nil, fmt.Errorf("membership %q maps to a different window or value", candidate.Key.EventID)
			}
			continue
		}
		if owner, ok := l.claims[candidate.Key]; ok && owner != reservation {
			return nil, nil, fmt.Errorf("membership %q is reserved by another window", candidate.Key.EventID)
		}
	}

	type projectedState struct {
		count    uint64
		distinct map[string]uint64
	}
	projected := make(map[WindowKey]*projectedState)
	seen = make(map[ContributionKey]Candidate)
	previews := make([]Preview, 0, len(candidates))
	staged := make([]Candidate, 0, len(candidates))

	for _, candidate := range candidates {
		state := projected[candidate.Window]
		if state == nil {
			slot := l.windows[candidate.Window]
			state = &projectedState{count: slot.count, distinct: cloneCounts(slot.distinct)}
			projected[candidate.Window] = state
		}

		willContribute := false
		if _, ok := seen[candidate.Key]; ok {
		} else if _, ok := l.memberships[candidate.Key]; ok {
			seen[candidate.Key] = candidate
		} else {
			seen[candidate.Key] = candidate
			l.claims[candidate.Key] = reservation
			staged = append(staged, candidate)
			state.count++
			state.distinct[candidate.DistinctKey]++
			willContribute = true
		}

		previews = append(previews, Preview{
			Candidate: candidate,
			Snapshot: Snapshot{
				Count:         state.count,
				DistinctCount: uint64(len(state.distinct)),
			},
			WillContribute: willContribute,
		})
	}
	return previews, staged, nil
}

func (l *Ledger) window(key WindowKey) *windowSlot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.windows == nil {
		l.ensureMapsLocked()
	}
	if slot := l.windows[key]; slot != nil {
		return slot
	}
	slot := &windowSlot{guard: make(chan struct{}, 1), distinct: make(map[string]uint64)}
	slot.guard <- struct{}{}
	l.windows[key] = slot
	return slot
}

func (l *Ledger) ensureMapsLocked() {
	if l.windows == nil {
		l.windows = make(map[WindowKey]*windowSlot)
	}
	if l.memberships == nil {
		l.memberships = make(map[ContributionKey]memberRecord)
	}
	if l.claims == nil {
		l.claims = make(map[ContributionKey]*Reservation)
	}
	if l.pending == nil {
		l.pending = make(map[core.DeliveryID]*Reservation)
	}
	if l.deliveries == nil {
		l.deliveries = make(map[core.DeliveryID]*deliverySlot)
	}
}

func validateCandidate(deliveryID core.DeliveryID, candidate Candidate) error {
	if candidate.DeliveryID != deliveryID {
		return fmt.Errorf("candidate delivery id differs from batch delivery")
	}
	if !core.ValidEventID(candidate.Key.EventID) {
		return fmt.Errorf("event id is not canonical")
	}
	if err := validateWindowKey(candidate.Window); err != nil {
		return err
	}
	if candidate.Key.RuleID != candidate.Window.RuleID || candidate.Key.RuleVersion != candidate.Window.RuleVersion {
		return fmt.Errorf("membership rule identity differs from window")
	}
	if candidate.DistinctKey == "" || !utf8.ValidString(candidate.DistinctKey) {
		return fmt.Errorf("distinct key must be non-empty UTF-8")
	}
	if candidate.ObservedAt.IsZero() {
		return fmt.Errorf("observed time is required")
	}
	return nil
}

func validateWindowKey(key WindowKey) error {
	if key.RuleID == "" || !utf8.ValidString(string(key.RuleID)) {
		return fmt.Errorf("rule id must be non-empty UTF-8")
	}
	if key.RuleVersion == "" || !utf8.ValidString(string(key.RuleVersion)) {
		return fmt.Errorf("rule version must be non-empty UTF-8")
	}
	if key.GroupKey == "" || !utf8.ValidString(key.GroupKey) {
		return fmt.Errorf("group key must be non-empty UTF-8")
	}
	return nil
}

func uniqueSortedWindowKeys(candidates []Candidate) []WindowKey {
	unique := make(map[WindowKey]struct{}, len(candidates))
	for _, candidate := range candidates {
		unique[candidate.Window] = struct{}{}
	}
	keys := make([]WindowKey, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].RuleID != keys[j].RuleID {
			return keys[i].RuleID < keys[j].RuleID
		}
		if keys[i].RuleVersion != keys[j].RuleVersion {
			return keys[i].RuleVersion < keys[j].RuleVersion
		}
		return keys[i].GroupKey < keys[j].GroupKey
	})
	return keys
}

func sameCandidateMapping(left, right Candidate) bool {
	return left.Window == right.Window && left.DistinctKey == right.DistinctKey && left.ObservedAt.Equal(right.ObservedAt)
}

func sameMemberMapping(member memberRecord, candidate Candidate) bool {
	return member.window == candidate.Window && member.distinctKey == candidate.DistinctKey && member.observedAt.Equal(candidate.ObservedAt)
}

func snapshotOf(slot *windowSlot) Snapshot {
	return Snapshot{Count: slot.count, DistinctCount: uint64(len(slot.distinct))}
}

func cloneCounts(source map[string]uint64) map[string]uint64 {
	clone := make(map[string]uint64, len(source))
	for key, count := range source {
		clone[key] = count
	}
	return clone
}

func releaseSlots(slots []*windowSlot) {
	for index := len(slots) - 1; index >= 0; index-- {
		releaseSlot(slots[index])
	}
}

func releaseSlot(slot *windowSlot) {
	slot.guard <- struct{}{}
}
