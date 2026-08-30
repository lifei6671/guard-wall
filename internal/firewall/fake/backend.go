// Package fake implements the in-memory Firewall boundary used by the M0 C2 slice.
// It deliberately proves no nftables, IPC, privilege, or packet-path behavior.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/enforcement"
)

// Domain assigns every external mutation to exactly one retry budget.
type Domain uint8

const (
	DomainInfrastructure Domain = iota + 1
	DomainPolicy
	DomainTarget
)

// ResultKind distinguishes authoritative success, known rejection, and ambiguous outcome.
type ResultKind uint8

const (
	ResultConfirmed ResultKind = iota + 1
	ResultRejected
	ResultUnknown
)

// PhysicalInfrastructure is the fake's externally observable infrastructure state.
type PhysicalInfrastructure struct {
	Backend      string
	OwnerVersion string
	Digest       string
}

// PhysicalPolicy is the fake's externally observable managed policy state.
type PhysicalPolicy struct {
	RelationDigest string
}

// OperationPlan is one domain-scoped fake mutation. A target plan contains one target only.
type OperationPlan struct {
	Domain                         Domain
	Target                         netip.Prefix
	DesiredTarget                  core.NormalizedTargetEnforcementIntent
	DesiredInfrastructure          core.ManagedInfrastructureIntent
	DesiredPolicy                  core.ManagedPolicyIntent
	ExpectedInfrastructureRevision core.InfrastructureRevision
	ExpectedPolicyRevision         core.PolicyRevision
	ExpectedTargetGeneration       core.TargetEnforcementGeneration
	ExpectedSnapshotRevision       core.SnapshotRevision
	FenceSnapshotRevision          bool
	BasisSnapshotDigest            string
	Digest                         string
}

// ApplyResult reports only the physical fake operation outcome; DB fencing belongs to Reconcile.
type ApplyResult struct {
	Kind      ResultKind
	Domain    Domain
	Target    netip.Prefix
	Digest    string
	ErrorCode string
}

// QueuedOutcome controls one subsequent Apply. Unknown may mutate first to model an ambiguous
// timeout after dispatch.
type QueuedOutcome struct {
	Kind      ResultKind
	Mutate    bool
	ErrorCode string
}

// Snapshot is a deep copy of the current fake physical state. It contains no application
// revision or target generation.
type Snapshot struct {
	Infrastructure *PhysicalInfrastructure
	Policy         *PhysicalPolicy
	Targets        map[netip.Prefix]core.PhysicalTargetObserved
}

// Digest returns a canonical physical-snapshot digest suitable for binding a freshly rebuilt plan.
func (s Snapshot) Digest() string {
	hash := sha256.New()
	if s.Infrastructure == nil {
		fmt.Fprint(hash, "infra:nil\n")
	} else {
		fmt.Fprintf(hash, "infra:%q:%q:%q\n", s.Infrastructure.Backend, s.Infrastructure.OwnerVersion, s.Infrastructure.Digest)
	}
	if s.Policy == nil {
		fmt.Fprint(hash, "policy:nil\n")
	} else {
		fmt.Fprintf(hash, "policy:%q\n", s.Policy.RelationDigest)
	}
	targets := make([]netip.Prefix, 0, len(s.Targets))
	for target := range s.Targets {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(left, right int) bool { return targets[left].String() < targets[right].String() })
	for _, target := range targets {
		observed := s.Targets[target]
		fmt.Fprintf(hash, "target:%s:%d:%d:%q:%d:", target, observed.BanMembership, observed.PolicyCoverage, observed.PolicyRelationDigest, observed.TimeoutMode)
		writeTime(hash, observed.NativeExpiry)
		fmt.Fprintf(hash, ":%d:%d:%q\n", observed.Scopes, observed.AddressFamily, observed.OwnerVersion)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

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

// PlanDigest binds one operation's payload, fences, and optional fresh physical basis.
func PlanDigest(plan OperationPlan) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "domain:%d\ntarget:%s\ninfra-rev:%d\npolicy-rev:%d\ntarget-gen:%d\nsnapshot-rev:%d\nfence-snapshot:%t\nbasis:%q\n",
		plan.Domain, plan.Target, plan.ExpectedInfrastructureRevision, plan.ExpectedPolicyRevision,
		plan.ExpectedTargetGeneration, plan.ExpectedSnapshotRevision, plan.FenceSnapshotRevision, plan.BasisSnapshotDigest)
	fmt.Fprintf(hash, "infra:%q:%q:%q\n", plan.DesiredInfrastructure.Backend, plan.DesiredInfrastructure.OwnerVersion, plan.DesiredInfrastructure.Digest)
	fmt.Fprintf(hash, "policy:%q\n", plan.DesiredPolicy.RelationDigest)
	intent := plan.DesiredTarget
	fmt.Fprintf(hash, "intent:%q:%s:%d:", intent.NodeID, intent.CanonicalTarget, intent.BanMembership)
	writeTime(hash, intent.EffectiveUntil)
	fmt.Fprintf(hash, ":%d:%d:%d:%d:%q:%q:%d\n", intent.TimeoutMode, intent.Scopes, intent.AddressFamily,
		intent.PolicyCoverage, intent.PolicyRelationDigest, intent.BackendAttributesDigest, intent.Generation)
	return hex.EncodeToString(hash.Sum(nil))
}

// ValidatePlan validates both its domain payload and stable digest.
func ValidatePlan(plan OperationPlan) error {
	if !validDomain(plan.Domain) {
		return fmt.Errorf("unknown failure domain %d", plan.Domain)
	}
	if plan.Digest == "" || plan.Digest != PlanDigest(plan) {
		return fmt.Errorf("plan digest does not bind its payload")
	}
	switch plan.Domain {
	case DomainInfrastructure:
		if plan.Target.IsValid() || plan.ExpectedInfrastructureRevision == 0 {
			return fmt.Errorf("infrastructure plan has an invalid fence or target")
		}
		if plan.DesiredInfrastructure.Backend == "" || plan.DesiredInfrastructure.OwnerVersion == "" || plan.DesiredInfrastructure.Digest == "" {
			return fmt.Errorf("infrastructure plan requires a complete desired payload")
		}
	case DomainPolicy:
		if plan.Target.IsValid() || plan.ExpectedPolicyRevision == 0 {
			return fmt.Errorf("policy plan has an invalid fence or target")
		}
		if plan.DesiredPolicy.RelationDigest == "" {
			return fmt.Errorf("policy plan requires a relation digest")
		}
	case DomainTarget:
		if !plan.Target.IsValid() || plan.Target != plan.Target.Masked() {
			return fmt.Errorf("target plan requires one canonical target")
		}
		if plan.DesiredTarget.CanonicalTarget != plan.Target || plan.ExpectedTargetGeneration == 0 ||
			plan.DesiredTarget.Generation != plan.ExpectedTargetGeneration {
			return fmt.Errorf("target plan payload generation does not match its fence")
		}
		if err := plan.DesiredTarget.Validate(); err != nil {
			return fmt.Errorf("validate desired target: %w", err)
		}
	}
	return nil
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
