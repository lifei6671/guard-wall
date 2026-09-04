package reconcile

import "github.com/lifei6671/guard-wall/internal/reconcile/model"

type Domain = model.Domain
type ResultKind = model.ResultKind
type PhysicalInfrastructure = model.PhysicalInfrastructure
type PhysicalPolicy = model.PhysicalPolicy
type OperationPlan = model.OperationPlan
type ApplyResult = model.ApplyResult
type Snapshot = model.Snapshot

// freshBasisBackend is an opt-in physical boundary whose Apply authorization
// requires a newly observed basis for every mutation.
type freshBasisBackend interface{ RequiresFreshBasis() bool }

const (
	DomainInfrastructure = model.DomainInfrastructure
	DomainPolicy         = model.DomainPolicy
	DomainTarget         = model.DomainTarget
	ResultConfirmed      = model.ResultConfirmed
	ResultRejected       = model.ResultRejected
	ResultUnknown        = model.ResultUnknown
)

func PlanDigest(plan OperationPlan) string { return model.PlanDigest(plan) }

func ValidatePlan(plan OperationPlan) error { return model.ValidatePlan(plan) }

func validDomain(domain Domain) bool { return model.ValidDomain(domain) }
