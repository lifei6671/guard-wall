// Package model owns Reconcile's portable plan and physical-observation types.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"sort"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// Domain assigns every physical mutation to one independent retry budget.
type Domain uint8

const (
	DomainInfrastructure Domain = iota + 1
	DomainPolicy
	DomainTarget
)

// ResultKind distinguishes a confirmed result, a known rejection, and an
// indeterminate result that requires a fresh Probe before another mutation.
type ResultKind uint8

const (
	ResultConfirmed ResultKind = iota + 1
	ResultRejected
	ResultUnknown
)

// PhysicalInfrastructure is the reconciler's minimum infrastructure observation.
type PhysicalInfrastructure struct {
	Backend      string
	OwnerVersion string
	Digest       string
}

// PhysicalPolicy is the reconciler's minimum policy observation.
type PhysicalPolicy struct {
	RelationDigest string
}

// OperationPlan binds one domain-scoped Desired mutation to its revision fences
// and, when available, an authoritative physical basis digest.
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

// ApplyResult reports only one physical operation outcome. Durable retry and
// observed-state fencing remain the Controller's responsibility.
type ApplyResult struct {
	Kind      ResultKind
	Domain    Domain
	Target    netip.Prefix
	Digest    string
	ErrorCode string
}

// Snapshot is the reconciler's immutable physical observation. Target values
// may carry either complete or managed-snapshot-limited evidence; matching is
// deliberately evidence-aware and never fills fields from Desired state.
type Snapshot struct {
	// BasisDigest preserves the production ManagedSnapshot digest used by the
	// Enforcer's authorization boundary. Fake snapshots leave it empty and use
	// the deterministic Reconcile representation below.
	BasisDigest    string
	Infrastructure *PhysicalInfrastructure
	Policy         *PhysicalPolicy
	Targets        map[netip.Prefix]core.PhysicalTargetObserved
}

// Digest returns a canonical physical-snapshot digest suitable for binding a
// freshly rebuilt Plan.
func (s Snapshot) Digest() string {
	if s.BasisDigest != "" {
		return s.BasisDigest
	}
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
		writePlanTime(hash, observed.NativeExpiry)
		fmt.Fprintf(hash, ":%d:%d:%q:%d\n", observed.Scopes, observed.AddressFamily, observed.OwnerVersion, observed.Evidence)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// PlanDigest binds an operation's payload, fences, and optional fresh physical basis.
func PlanDigest(plan OperationPlan) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "domain:%d\ntarget:%s\ninfra-rev:%d\npolicy-rev:%d\ntarget-gen:%d\nsnapshot-rev:%d\nfence-snapshot:%t\nbasis:%q\n",
		plan.Domain, plan.Target, plan.ExpectedInfrastructureRevision, plan.ExpectedPolicyRevision,
		plan.ExpectedTargetGeneration, plan.ExpectedSnapshotRevision, plan.FenceSnapshotRevision, plan.BasisSnapshotDigest)
	fmt.Fprintf(hash, "infra:%q:%q:%q\n", plan.DesiredInfrastructure.Backend, plan.DesiredInfrastructure.OwnerVersion, plan.DesiredInfrastructure.Digest)
	fmt.Fprintf(hash, "policy:%q\n", plan.DesiredPolicy.RelationDigest)
	intent := plan.DesiredTarget
	fmt.Fprintf(hash, "intent:%q:%s:%d:", intent.NodeID, intent.CanonicalTarget, intent.BanMembership)
	writePlanTime(hash, intent.EffectiveUntil)
	fmt.Fprintf(hash, ":%d:%d:%d:%d:%q:%q:%d\n", intent.TimeoutMode, intent.Scopes, intent.AddressFamily,
		intent.PolicyCoverage, intent.PolicyRelationDigest, intent.BackendAttributesDigest, intent.Generation)
	return hex.EncodeToString(hash.Sum(nil))
}

// ValidatePlan validates both a domain payload and its stable digest.
func ValidatePlan(plan OperationPlan) error {
	if !ValidDomain(plan.Domain) {
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
		if plan.DesiredPolicy.Allowlist != nil || plan.DesiredPolicy.ProtectedTargets != nil {
			if err := plan.DesiredPolicy.ValidateComplete(); err != nil {
				return fmt.Errorf("policy plan requires a complete desired payload: %w", err)
			}
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

// ValidDomain reports whether a value is one of Reconcile's closed mutation domains.
func ValidDomain(domain Domain) bool {
	return domain == DomainInfrastructure || domain == DomainPolicy || domain == DomainTarget
}

func writePlanTime(writer interface{ Write([]byte) (int, error) }, value *time.Time) {
	if value == nil {
		fmt.Fprint(writer, "nil")
		return
	}
	fmt.Fprint(writer, value.UTC().Format(time.RFC3339Nano))
}
