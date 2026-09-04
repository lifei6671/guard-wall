// Package fake implements the in-memory Firewall boundary used by the M0 C2 slice.
// It deliberately proves no nftables, IPC, privilege, or packet-path behavior.
package fake

import (
	"context"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
	"github.com/lifei6671/guard-wall/internal/reconcile/model"
)

// Domain Reconcile owns all plan, result, and physical-observation semantics. The
// fake exposes aliases solely as a test simulator, so production Reconcile no
// longer imports this package.
type Domain = model.Domain
type ResultKind = model.ResultKind
type PhysicalInfrastructure = model.PhysicalInfrastructure
type PhysicalPolicy = model.PhysicalPolicy
type OperationPlan = model.OperationPlan
type ApplyResult = model.ApplyResult
type Snapshot = model.Snapshot

const (
	DomainInfrastructure = model.DomainInfrastructure
	DomainPolicy         = model.DomainPolicy
	DomainTarget         = model.DomainTarget
	ResultConfirmed      = model.ResultConfirmed
	ResultRejected       = model.ResultRejected
	ResultUnknown        = model.ResultUnknown
)

// QueuedOutcome controls one subsequent fake Apply. Unknown may mutate first
// to model an ambiguous timeout after dispatch.
type QueuedOutcome struct {
	Kind      ResultKind
	Mutate    bool
	ErrorCode string
}

// PlanDigest and ValidatePlan preserve the fake test API while delegating to
// the production-owned Reconcile definitions.
func PlanDigest(plan OperationPlan) string { return model.PlanDigest(plan) }

func ValidatePlan(plan OperationPlan) error { return model.ValidatePlan(plan) }

// Backend is an in-process external-state simulator.
type Backend struct {
	mu             sync.Mutex
	infrastructure *PhysicalInfrastructure
	policy         *PhysicalPolicy
	targets        map[netip.Prefix]core.PhysicalTargetObserved
	outcomes       map[Domain][]QueuedOutcome
	probeCount     uint64
	applyCount     uint64
}

// NewBackend returns an empty, healthy fake backend.
func NewBackend() *Backend {
	return &Backend{
		targets:  make(map[netip.Prefix]core.PhysicalTargetObserved),
		outcomes: make(map[Domain][]QueuedOutcome),
	}
}

// QueueOutcome appends a deterministic Apply result for one domain.
func (b *Backend) QueueOutcome(domain Domain, outcome QueuedOutcome) error {
	if err := validateOutcome(outcome); err != nil {
		return err
	}
	if !validDomain(domain) {
		return fmt.Errorf("unknown failure domain %d", domain)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.outcomes[domain] = append(b.outcomes[domain], outcome)
	return nil
}

// Probe returns an authoritative copy of fake physical state.
func (b *Backend) Probe(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeCount++
	return b.snapshotLocked(), nil
}

// Apply executes exactly one domain-scoped fake plan. A valid-domain plan validation error is a
// stable non-retryable rejection and never reaches the mutation path.
func (b *Backend) Apply(ctx context.Context, plan OperationPlan) (ApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	if err := ValidatePlan(plan); err != nil {
		if validDomain(plan.Domain) {
			return ApplyResult{Kind: ResultRejected, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest, ErrorCode: "invalid_plan"}, nil
		}
		return ApplyResult{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.applyCount++
	outcome := QueuedOutcome{Kind: ResultConfirmed, Mutate: true}
	if queued := b.outcomes[plan.Domain]; len(queued) > 0 {
		outcome = queued[0]
		b.outcomes[plan.Domain] = queued[1:]
	}
	if outcome.Mutate {
		b.applyPhysicalLocked(plan)
	}
	return ApplyResult{Kind: outcome.Kind, Domain: plan.Domain, Target: plan.Target, Digest: plan.Digest, ErrorCode: outcome.ErrorCode}, nil
}

// SetPhysicalTarget introduces physical drift without carrying an application generation.
func (b *Backend) SetPhysicalTarget(observed core.PhysicalTargetObserved) error {
	if err := validatePhysicalTarget(observed); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.targets[observed.CanonicalTarget] = clonePhysicalTarget(observed)
	return nil
}

// Counts returns probe and mutation call totals.
func (b *Backend) Counts() (probes, applies uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.probeCount, b.applyCount
}

func (b *Backend) applyPhysicalLocked(plan OperationPlan) {
	switch plan.Domain {
	case DomainInfrastructure:
		b.infrastructure = &PhysicalInfrastructure{
			Backend: plan.DesiredInfrastructure.Backend, OwnerVersion: plan.DesiredInfrastructure.OwnerVersion, Digest: plan.DesiredInfrastructure.Digest,
		}
	case DomainPolicy:
		b.policy = &PhysicalPolicy{RelationDigest: plan.DesiredPolicy.RelationDigest}
	case DomainTarget:
		if plan.DesiredTarget.BanMembership == core.BanAbsent {
			delete(b.targets, plan.Target)
			return
		}
		b.targets[plan.Target] = physicalFromIntent(plan.DesiredTarget)
	}
}

func (b *Backend) snapshotLocked() Snapshot {
	snapshot := Snapshot{Targets: make(map[netip.Prefix]core.PhysicalTargetObserved, len(b.targets))}
	if b.infrastructure != nil {
		value := *b.infrastructure
		snapshot.Infrastructure = &value
	}
	if b.policy != nil {
		value := *b.policy
		snapshot.Policy = &value
	}
	for target, observed := range b.targets {
		snapshot.Targets[target] = clonePhysicalTarget(observed)
	}
	return snapshot
}

func physicalFromIntent(intent core.NormalizedTargetEnforcementIntent) core.PhysicalTargetObserved {
	coverage := core.ObservedPolicyNone
	switch intent.PolicyCoverage {
	case core.PolicyCoveragePartial:
		coverage = core.ObservedPolicyPartial
	case core.PolicyCoverageFull:
		coverage = core.ObservedPolicyFull
	}
	observed := core.PhysicalTargetObserved{
		CanonicalTarget:      intent.CanonicalTarget,
		ObservedAt:           time.Now().UTC(),
		Backend:              "fake",
		BanMembership:        core.ObservedMembershipPresent,
		PolicyCoverage:       coverage,
		PolicyRelationDigest: intent.PolicyRelationDigest,
		TimeoutMode:          intent.TimeoutMode,
		Scopes:               intent.Scopes,
		AddressFamily:        intent.AddressFamily,
		OwnerVersion:         intent.BackendAttributesDigest,
	}
	if intent.TimeoutMode == core.TimeoutNative {
		observed.NativeExpiry = enforcement.NativeExpiryForIntent(intent)
	}
	return observed
}

func validatePhysicalTarget(observed core.PhysicalTargetObserved) error {
	if !observed.CanonicalTarget.IsValid() || observed.CanonicalTarget != observed.CanonicalTarget.Masked() {
		return fmt.Errorf("physical target must be canonical")
	}
	if observed.BanMembership != core.ObservedMembershipAbsent && observed.BanMembership != core.ObservedMembershipPresent && observed.BanMembership != core.ObservedMembershipUnknown {
		return fmt.Errorf("physical target membership is invalid")
	}
	return nil
}

func validateOutcome(outcome QueuedOutcome) error {
	switch outcome.Kind {
	case ResultConfirmed:
		if !outcome.Mutate {
			return fmt.Errorf("confirmed outcome must establish its postcondition")
		}
	case ResultRejected:
		if outcome.Mutate {
			return fmt.Errorf("rejected outcome cannot mutate fake state")
		}
	case ResultUnknown:
		// Both mutation and no mutation are valid ambiguous results.
	default:
		return fmt.Errorf("unknown result kind %d", outcome.Kind)
	}
	return nil
}

func validDomain(domain Domain) bool {
	return domain == DomainInfrastructure || domain == DomainPolicy || domain == DomainTarget
}

func clonePhysicalTarget(observed core.PhysicalTargetObserved) core.PhysicalTargetObserved {
	observed.NativeExpiry = cloneTime(observed.NativeExpiry)
	return observed
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func writeTime(writer interface{ Write([]byte) (int, error) }, value *time.Time) {
	if value == nil {
		fmt.Fprint(writer, "nil")
		return
	}
	fmt.Fprint(writer, value.UTC().Format(time.RFC3339Nano))
}
