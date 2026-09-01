package ipc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

const mutationRequestMaxBytes = 64 * 1024

func TestMutationRequestConstructorsMatchGoldenVectors(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		construct func(*testing.T) ipc.MutationRequest
	}{
		{
			name: "apply infrastructure",
			path: "valid/apply-infrastructure.json",
			construct: func(t *testing.T) ipc.MutationRequest {
				request, err := ipc.NewApplyInfrastructureRequest(strings.Repeat("a", 64), 1)
				if err != nil {
					t.Fatalf("NewApplyInfrastructureRequest(): %v", err)
				}
				return request
			},
		},
		{
			name: "apply policy",
			path: "valid/apply-policy.json",
			construct: func(t *testing.T) ipc.MutationRequest {
				request, err := ipc.NewApplyPolicyRequest(
					strings.Repeat("b", 64),
					1,
					[]netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")},
					[]netip.Prefix{
						netip.MustParsePrefix("127.0.0.0/8"),
						netip.MustParsePrefix("::1/128"),
					},
				)
				if err != nil {
					t.Fatalf("NewApplyPolicyRequest(): %v", err)
				}
				return request
			},
		},
		{
			name: "apply target",
			path: "valid/apply-target.json",
			construct: func(t *testing.T) ipc.MutationRequest {
				request, err := ipc.NewApplyTargetRequest(
					strings.Repeat("c", 64),
					1,
					netip.MustParsePrefix("192.0.2.4/32"),
					ipc.MembershipPresent,
					ipc.TimeoutModeNative,
					1700000000000000,
					true,
					[]ipc.Scope{ipc.ScopeInput},
				)
				if err != nil {
					t.Fatalf("NewApplyTargetRequest(): %v", err)
				}
				return request
			},
		},
		{
			name: "remove managed infrastructure",
			path: "valid/remove-managed-infrastructure.json",
			construct: func(*testing.T) ipc.MutationRequest {
				return ipc.NewRemoveManagedInfrastructureRequest()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := test.construct(t)
			assertMutationRequestFields(t, test.path, request)

			encoded, err := ipc.EncodeMutationRequest(request)
			if err != nil {
				t.Fatalf("EncodeMutationRequest(): %v", err)
			}
			want := compactMutationRequestGolden(t, test.path)
			if !bytes.Equal(encoded, want) {
				t.Fatalf("encoded request = %s, want exact golden %s", encoded, want)
			}

			for attempt := 0; attempt < 32; attempt++ {
				again, encodeErr := ipc.EncodeMutationRequest(request)
				if encodeErr != nil {
					t.Fatalf("deterministic encode %d: %v", attempt, encodeErr)
				}
				if !bytes.Equal(again, encoded) {
					t.Fatalf("encode %d = %s, want deterministic %s", attempt, again, encoded)
				}
			}

			decoded, err := ipc.DecodeRequest(encoded)
			if err != nil {
				t.Fatalf("DecodeRequest(encoded): %v", err)
			}
			mutation, ok := decoded.(ipc.MutationRequest)
			if !ok {
				t.Fatalf("decoded request type = %T, want ipc.MutationRequest", decoded)
			}
			assertMutationRequestFields(t, test.path, mutation)
			roundTrip, err := ipc.EncodeMutationRequest(mutation)
			if err != nil {
				t.Fatalf("EncodeMutationRequest(round trip): %v", err)
			}
			if !bytes.Equal(roundTrip, encoded) {
				t.Fatalf("round-trip request = %s, want %s", roundTrip, encoded)
			}
		})
	}
}

func TestMutationRequestConstructorsIsolateSliceStorage(t *testing.T) {
	allowlist := []netip.Prefix{
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
	protected := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	policy, err := ipc.NewApplyPolicyRequest(
		strings.Repeat("d", 64), 7, allowlist, protected,
	)
	if err != nil {
		t.Fatalf("NewApplyPolicyRequest(): %v", err)
	}
	policyPlan := policy.Plan().(ipc.PolicyPlan)
	policyBefore, err := ipc.EncodeMutationRequest(policy)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(policy): %v", err)
	}

	allowlist[0] = netip.MustParsePrefix("10.0.0.0/8")
	protected[0] = netip.MustParsePrefix("192.0.2.0/24")
	assertMutationRequestPrefixes(t, policyPlan.Allowlist(), []string{
		"198.51.100.0/24", "203.0.113.0/24",
	})
	assertMutationRequestPrefixes(t, policyPlan.ProtectedTargets(), []string{
		"127.0.0.0/8", "::1/128",
	})

	returnedAllowlist := policyPlan.Allowlist()
	returnedProtected := policyPlan.ProtectedTargets()
	returnedAllowlist[0] = netip.MustParsePrefix("10.0.0.0/8")
	returnedProtected[0] = netip.MustParsePrefix("192.0.2.0/24")
	assertMutationRequestPrefixes(t, policyPlan.Allowlist(), []string{
		"198.51.100.0/24", "203.0.113.0/24",
	})
	assertMutationRequestPrefixes(t, policyPlan.ProtectedTargets(), []string{
		"127.0.0.0/8", "::1/128",
	})
	policyAfter, err := ipc.EncodeMutationRequest(policy)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(policy after mutations): %v", err)
	}
	if !bytes.Equal(policyAfter, policyBefore) {
		t.Fatalf("policy encode changed through slice alias: before %s, after %s", policyBefore, policyAfter)
	}

	scopes := []ipc.Scope{ipc.ScopeInput, ipc.ScopeForward}
	target, err := ipc.NewApplyTargetRequest(
		strings.Repeat("e", 64),
		8,
		netip.MustParsePrefix("198.51.100.8/32"),
		ipc.MembershipAbsent,
		ipc.TimeoutModeNone,
		0,
		false,
		scopes,
	)
	if err != nil {
		t.Fatalf("NewApplyTargetRequest(): %v", err)
	}
	targetPlan := target.Plan().(ipc.TargetPlan)
	targetBefore, err := ipc.EncodeMutationRequest(target)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(target): %v", err)
	}
	scopes[0] = ipc.ScopeForward
	if got := targetPlan.Scopes(); !reflect.DeepEqual(got, []ipc.Scope{ipc.ScopeInput, ipc.ScopeForward}) {
		t.Fatalf("Scopes() after input mutation = %#v, want [input forward]", got)
	}
	returnedScopes := targetPlan.Scopes()
	returnedScopes[0] = ipc.ScopeForward
	if got := targetPlan.Scopes(); !reflect.DeepEqual(got, []ipc.Scope{ipc.ScopeInput, ipc.ScopeForward}) {
		t.Fatalf("Scopes() exposed mutable storage: %#v", got)
	}
	targetAfter, err := ipc.EncodeMutationRequest(target)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(target after mutations): %v", err)
	}
	if !bytes.Equal(targetAfter, targetBefore) {
		t.Fatalf("target encode changed through slice alias: before %s, after %s", targetBefore, targetAfter)
	}
}

func TestMutationRequestPolicyEmptyAllowlistEncodesAsArray(t *testing.T) {
	request, err := ipc.NewApplyPolicyRequest(
		strings.Repeat("a", 64),
		1,
		nil,
		[]netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		},
	)
	if err != nil {
		t.Fatalf("NewApplyPolicyRequest(): %v", err)
	}
	encoded, err := ipc.EncodeMutationRequest(request)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(): %v", err)
	}
	if !bytes.Contains(encoded, []byte(`"allowlist":[]`)) {
		t.Fatalf("empty allowlist encoded as non-array: %s", encoded)
	}
	if bytes.Contains(encoded, []byte(`"allowlist":null`)) {
		t.Fatalf("empty allowlist encoded as null: %s", encoded)
	}
}

func TestMutationRequestRoundTripPreservesCallerValues(t *testing.T) {
	t.Run("policy", func(t *testing.T) {
		request, err := ipc.NewApplyPolicyRequest(
			strings.Repeat("d", 64),
			17,
			[]netip.Prefix{
				netip.MustParsePrefix("198.18.0.0/15"),
				netip.MustParsePrefix("203.0.113.0/24"),
			},
			[]netip.Prefix{
				netip.MustParsePrefix("127.0.0.0/8"),
				netip.MustParsePrefix("192.0.2.0/24"),
				netip.MustParsePrefix("::1/128"),
			},
		)
		if err != nil {
			t.Fatalf("NewApplyPolicyRequest(): %v", err)
		}
		encoded, err := ipc.EncodeMutationRequest(request)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(): %v", err)
		}
		decoded, err := ipc.DecodeRequest(encoded)
		if err != nil {
			t.Fatalf("DecodeRequest(): %v", err)
		}
		apply, ok := decoded.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("decoded request type = %T, want ApplyManagedPlanRequest", decoded)
		}
		plan, ok := apply.Plan().(ipc.PolicyPlan)
		if !ok {
			t.Fatalf("decoded plan type = %T, want PolicyPlan", apply.Plan())
		}
		if got := plan.BasisSnapshotDigest(); got != strings.Repeat("d", 64) {
			t.Fatalf("BasisSnapshotDigest() = %q", got)
		}
		if got := plan.Revision(); got != 17 {
			t.Fatalf("Revision() = %d, want 17", got)
		}
		assertMutationRequestPrefixes(t, plan.Allowlist(), []string{"198.18.0.0/15", "203.0.113.0/24"})
		assertMutationRequestPrefixes(t, plan.ProtectedTargets(), []string{"127.0.0.0/8", "192.0.2.0/24", "::1/128"})
	})

	t.Run("target absent", func(t *testing.T) {
		request, err := ipc.NewApplyTargetRequest(
			strings.Repeat("e", 64),
			19,
			netip.MustParsePrefix("203.0.113.42/32"),
			ipc.MembershipAbsent,
			ipc.TimeoutModeNone,
			0,
			false,
			[]ipc.Scope{ipc.ScopeInput, ipc.ScopeForward},
		)
		if err != nil {
			t.Fatalf("NewApplyTargetRequest(): %v", err)
		}
		encoded, err := ipc.EncodeMutationRequest(request)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(): %v", err)
		}
		if !bytes.Contains(encoded, []byte(`"effective_until_unix_us":null`)) {
			t.Fatalf("absent target expiry was not explicit null: %s", encoded)
		}
		decoded, err := ipc.DecodeRequest(encoded)
		if err != nil {
			t.Fatalf("DecodeRequest(): %v", err)
		}
		apply, ok := decoded.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("decoded request type = %T, want ApplyManagedPlanRequest", decoded)
		}
		plan, ok := apply.Plan().(ipc.TargetPlan)
		if !ok {
			t.Fatalf("decoded plan type = %T, want TargetPlan", apply.Plan())
		}
		if got := plan.BasisSnapshotDigest(); got != strings.Repeat("e", 64) {
			t.Fatalf("BasisSnapshotDigest() = %q", got)
		}
		if got := plan.Revision(); got != 19 {
			t.Fatalf("Revision() = %d, want 19", got)
		}
		if got := plan.Target(); got != netip.MustParsePrefix("203.0.113.42/32") {
			t.Fatalf("Target() = %v", got)
		}
		if got := plan.Membership(); got != ipc.MembershipAbsent {
			t.Fatalf("Membership() = %q, want absent", got)
		}
		if got := plan.TimeoutMode(); got != ipc.TimeoutModeNone {
			t.Fatalf("TimeoutMode() = %q, want none", got)
		}
		if expiry, found := plan.EffectiveUntilUnixMicro(); expiry != 0 || found {
			t.Fatalf("EffectiveUntilUnixMicro() = %d, %v, want 0, false", expiry, found)
		}
		if got := plan.Scopes(); !reflect.DeepEqual(got, []ipc.Scope{ipc.ScopeInput, ipc.ScopeForward}) {
			t.Fatalf("Scopes() = %#v, want [input forward]", got)
		}
	})
}

func TestMutationRequestConstructorsRejectInvalidInputs(t *testing.T) {
	digest := strings.Repeat("f", 64)
	loopbacks := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	}
	normalTarget := netip.MustParsePrefix("198.51.100.8/32")
	tests := []struct {
		name string
		code ipc.ErrorCode
		call func() error
	}{
		{
			name: "digest length",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyInfrastructureRequest("short", 1)
				return err
			},
		},
		{
			name: "digest alphabet",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyInfrastructureRequest(strings.Repeat("G", 64), 1)
				return err
			},
		},
		{
			name: "zero revision",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyInfrastructureRequest(digest, 0)
				return err
			},
		},
		{
			name: "negative revision",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyPolicyRequest(digest, -1, nil, loopbacks)
				return err
			},
		},
		{
			name: "noncanonical policy prefix",
			code: ipc.ErrorCodeNoncanonicalPrefix,
			call: func() error {
				_, err := ipc.NewApplyPolicyRequest(
					digest, 1,
					[]netip.Prefix{netip.MustParsePrefix("198.51.100.1/24")},
					loopbacks,
				)
				return err
			},
		},
		{
			name: "unsorted policy prefixes",
			code: ipc.ErrorCodeNoncanonicalOrder,
			call: func() error {
				_, err := ipc.NewApplyPolicyRequest(
					digest, 1,
					[]netip.Prefix{
						netip.MustParsePrefix("203.0.113.0/24"),
						netip.MustParsePrefix("198.51.100.0/24"),
					},
					loopbacks,
				)
				return err
			},
		},
		{
			name: "duplicate policy prefix",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				prefix := netip.MustParsePrefix("198.51.100.0/24")
				_, err := ipc.NewApplyPolicyRequest(
					digest, 1, []netip.Prefix{prefix, prefix}, loopbacks,
				)
				return err
			},
		},
		{
			name: "mandatory protected target missing",
			code: ipc.ErrorCodeProtectedPolicyMissing,
			call: func() error {
				_, err := ipc.NewApplyPolicyRequest(
					digest, 1, nil,
					[]netip.Prefix{
						netip.MustParsePrefix("127.0.0.0/8"),
						netip.MustParsePrefix("192.0.2.0/24"),
					},
				)
				return err
			},
		},
		{
			name: "protected loopback target",
			code: ipc.ErrorCodeProtectedTarget,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, netip.MustParsePrefix("127.0.0.1/32"),
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "noncanonical target prefix",
			code: ipc.ErrorCodeNoncanonicalPrefix,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, netip.MustParsePrefix("198.51.100.9/24"),
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "invalid membership",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.Membership("toggle"), ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "invalid timeout enum",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutMode("userspace"), 0, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "absent native timeout",
			code: ipc.ErrorCodeInvalidTimeout,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipAbsent, ipc.TimeoutModeNative, 1700000000000000, true,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "native timeout without expiry",
			code: ipc.ErrorCodeInvalidTimeout,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNative, 0, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "none timeout with expiry",
			code: ipc.ErrorCodeInvalidTimeout,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 1700000000000000, true,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "hidden expiry value",
			code: ipc.ErrorCodeInvalidTimeout,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 1700000000000000, false,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "nonpositive expiry",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNative, 0, true,
					[]ipc.Scope{ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "empty scopes",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false, nil,
				)
				return err
			},
		},
		{
			name: "duplicate scope",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.ScopeInput, ipc.ScopeInput},
				)
				return err
			},
		},
		{
			name: "invalid scope",
			code: ipc.ErrorCodeSchemaRejected,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.Scope("output")},
				)
				return err
			},
		},
		{
			name: "noncanonical scope order",
			code: ipc.ErrorCodeInvalidScope,
			call: func() error {
				_, err := ipc.NewApplyTargetRequest(
					digest, 1, normalTarget,
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false,
					[]ipc.Scope{ipc.ScopeForward, ipc.ScopeInput},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertMutationRequestValidationError(t, test.call(), test.code)
		})
	}
}

func TestMutationRequestEncodeRejectsNilWithoutPanic(t *testing.T) {
	var mutation ipc.MutationRequest
	var apply ipc.ApplyManagedPlanRequest
	var remove ipc.RemoveManagedInfrastructureRequest
	concreteApply, err := ipc.NewApplyInfrastructureRequest(strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatalf("NewApplyInfrastructureRequest(): %v", err)
	}
	concreteApplyNil := reflect.Zero(reflect.TypeOf(concreteApply)).Interface().(ipc.MutationRequest)
	concreteRemove := ipc.NewRemoveManagedInfrastructureRequest()
	concreteRemoveNil := reflect.Zero(reflect.TypeOf(concreteRemove)).Interface().(ipc.MutationRequest)
	tests := []struct {
		name    string
		request ipc.MutationRequest
	}{
		{name: "untyped mutation nil", request: mutation},
		{name: "nil apply interface", request: apply},
		{name: "nil remove interface", request: remove},
		{name: "typed concrete apply nil", request: concreteApplyNil},
		{name: "typed concrete remove nil", request: concreteRemoveNil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("EncodeMutationRequest() panicked: %v", recovered)
				}
			}()
			raw, err := ipc.EncodeMutationRequest(test.request)
			if len(raw) != 0 {
				t.Fatalf("EncodeMutationRequest(nil) bytes = %q, want empty", raw)
			}
			assertMutationRequestValidationError(t, err, ipc.ErrorCodeSchemaRejected)
		})
	}
}

func TestMutationRequestConstructorsReturnNilOnError(t *testing.T) {
	digest := strings.Repeat("a", 64)
	checks := []struct {
		name string
		call func() (ipc.MutationRequest, error)
	}{
		{
			name: "infrastructure",
			call: func() (ipc.MutationRequest, error) {
				return ipc.NewApplyInfrastructureRequest("invalid", 1)
			},
		},
		{
			name: "policy",
			call: func() (ipc.MutationRequest, error) {
				return ipc.NewApplyPolicyRequest(digest, 1, nil, nil)
			},
		},
		{
			name: "target",
			call: func() (ipc.MutationRequest, error) {
				return ipc.NewApplyTargetRequest(
					digest, 1, netip.MustParsePrefix("192.0.2.4/32"),
					ipc.MembershipPresent, ipc.TimeoutModeNone, 0, false, nil,
				)
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			request, err := check.call()
			if err == nil {
				t.Fatal("constructor error = nil")
			}
			if request != nil {
				t.Fatalf("constructor request = %#v, want nil", request)
			}
		})
	}
}

func TestMutationRequestResourceLimits(t *testing.T) {
	t.Run("64 KiB exact and one-over", func(t *testing.T) {
		request := ipc.NewRemoveManagedInfrastructureRequest()
		encoded, err := ipc.EncodeMutationRequest(request)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(): %v", err)
		}
		exact := append(append([]byte(nil), encoded...), bytes.Repeat([]byte(" "), mutationRequestMaxBytes-len(encoded))...)
		decoded, err := ipc.DecodeRequest(exact)
		if err != nil {
			t.Fatalf("DecodeRequest(exact request limit): %v", err)
		}
		if _, ok := decoded.(ipc.MutationRequest); !ok {
			t.Fatalf("exact-limit decoded type = %T, want ipc.MutationRequest", decoded)
		}
		oneOver, err := ipc.DecodeRequest(append(exact, ' '))
		if oneOver != nil {
			t.Fatalf("one-over request = %#v, want nil", oneOver)
		}
		assertMutationRequestValidationError(t, err, ipc.ErrorCodeRequestTooLarge)
	})

	t.Run("1024 prefixes exact and one-over", func(t *testing.T) {
		protected := []netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		}
		exactAllowlist := mutationRequestPrefixes(1022)
		request, err := ipc.NewApplyPolicyRequest(
			strings.Repeat("a", 64), 1, exactAllowlist, protected,
		)
		if err != nil {
			t.Fatalf("exact prefix limit rejected: %v", err)
		}
		plan := request.Plan().(ipc.PolicyPlan)
		if got := len(plan.Allowlist()) + len(plan.ProtectedTargets()); got != 1024 {
			t.Fatalf("accepted prefix count = %d, want 1024", got)
		}
		encoded, err := ipc.EncodeMutationRequest(request)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(exact prefixes): %v", err)
		}
		if len(encoded) > mutationRequestMaxBytes {
			t.Fatalf("exact prefix request bytes = %d, want <= %d", len(encoded), mutationRequestMaxBytes)
		}
		if decoded, decodeErr := ipc.DecodeRequest(encoded); decodeErr != nil || decoded == nil {
			t.Fatalf("DecodeRequest(exact prefixes) = (%T, %v), want valid", decoded, decodeErr)
		}

		_, err = ipc.NewApplyPolicyRequest(
			strings.Repeat("a", 64), 1, mutationRequestPrefixes(1023), protected,
		)
		assertMutationRequestValidationError(t, err, ipc.ErrorCodePrefixLimit)

		_, err = ipc.NewApplyPolicyRequest(
			strings.Repeat("a", 64), 1, mutationRequestPrefixes(1025), protected,
		)
		assertMutationRequestValidationError(t, err, ipc.ErrorCodeSchemaRejected)

		overlargeProtected := append(mutationRequestPrefixes(1023), protected...)
		sort.Slice(overlargeProtected, func(left, right int) bool {
			return overlargeProtected[left].String() < overlargeProtected[right].String()
		})
		_, err = ipc.NewApplyPolicyRequest(
			strings.Repeat("a", 64), 1, nil, overlargeProtected,
		)
		assertMutationRequestValidationError(t, err, ipc.ErrorCodeSchemaRejected)
	})
}

func TestMutationRequestErrorsDoNotEchoInput(t *testing.T) {
	const marker = "do-not-echo-request-secret-913ee94c"
	digest := strings.Repeat(marker, 4)
	request, err := ipc.NewApplyInfrastructureRequest(digest, 1)
	if request != nil {
		t.Fatalf("invalid digest request = %#v, want nil", request)
	}
	assertMutationRequestValidationError(t, err, ipc.ErrorCodeSchemaRejected)
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), digest) {
		t.Fatalf("constructor error echoed attacker-controlled input: %q", err)
	}
}

func FuzzMutationRequestEncodeRoundTrip(f *testing.F) {
	for _, path := range []string{
		"valid/apply-infrastructure.json",
		"valid/apply-policy.json",
		"valid/apply-target.json",
		"valid/remove-managed-infrastructure.json",
		"invalid/command-field.json",
		"invalid/duplicate-key.json",
	} {
		f.Add(readGoldenFile(f, path))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		request, err := ipc.DecodeRequest(raw)
		if err != nil {
			if request != nil {
				t.Fatalf("rejected input returned request %#v", request)
			}
			var validationError *ipc.ValidationError
			if !errors.As(err, &validationError) || validationError.Code() == "" {
				t.Fatalf("DecodeRequest() error = %T %v, want classified *ipc.ValidationError", err, err)
			}
			return
		}

		mutation, ok := request.(ipc.MutationRequest)
		if !ok {
			return
		}
		encoded, err := ipc.EncodeMutationRequest(mutation)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(decoded %T): %v", mutation, err)
		}
		decoded, err := ipc.DecodeRequest(encoded)
		if err != nil {
			t.Fatalf("DecodeRequest(encoded): %v", err)
		}
		roundTrip, ok := decoded.(ipc.MutationRequest)
		if !ok {
			t.Fatalf("round-trip request type = %T, want ipc.MutationRequest", decoded)
		}
		again, err := ipc.EncodeMutationRequest(roundTrip)
		if err != nil {
			t.Fatalf("EncodeMutationRequest(round trip): %v", err)
		}
		if !bytes.Equal(again, encoded) {
			t.Fatalf("nondeterministic round trip: first %s, second %s", encoded, again)
		}
	})
}

func assertMutationRequestFields(t *testing.T, path string, request ipc.MutationRequest) {
	t.Helper()
	if request == nil {
		t.Fatal("mutation request = nil")
	}
	switch path {
	case "valid/apply-infrastructure.json":
		apply, ok := request.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("request type = %T, want ipc.ApplyManagedPlanRequest", request)
		}
		plan, ok := apply.Plan().(ipc.InfrastructurePlan)
		if !ok {
			t.Fatalf("plan type = %T, want ipc.InfrastructurePlan", apply.Plan())
		}
		assertMutationRequestPlanBase(t, request, plan, ipc.DomainInfrastructure, strings.Repeat("a", 64), 1)
		if got := plan.SchemaVersion(); got != 1 {
			t.Fatalf("SchemaVersion() = %d, want 1", got)
		}
	case "valid/apply-policy.json":
		apply, ok := request.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("request type = %T, want ipc.ApplyManagedPlanRequest", request)
		}
		plan, ok := apply.Plan().(ipc.PolicyPlan)
		if !ok {
			t.Fatalf("plan type = %T, want ipc.PolicyPlan", apply.Plan())
		}
		assertMutationRequestPlanBase(t, request, plan, ipc.DomainPolicy, strings.Repeat("b", 64), 1)
		assertMutationRequestPrefixes(t, plan.Allowlist(), []string{"198.51.100.0/24"})
		assertMutationRequestPrefixes(t, plan.ProtectedTargets(), []string{"127.0.0.0/8", "::1/128"})
	case "valid/apply-target.json":
		apply, ok := request.(ipc.ApplyManagedPlanRequest)
		if !ok {
			t.Fatalf("request type = %T, want ipc.ApplyManagedPlanRequest", request)
		}
		plan, ok := apply.Plan().(ipc.TargetPlan)
		if !ok {
			t.Fatalf("plan type = %T, want ipc.TargetPlan", apply.Plan())
		}
		assertMutationRequestPlanBase(t, request, plan, ipc.DomainTarget, strings.Repeat("c", 64), 1)
		if got, want := plan.Target(), netip.MustParsePrefix("192.0.2.4/32"); got != want {
			t.Fatalf("Target() = %v, want %v", got, want)
		}
		if got := plan.Membership(); got != ipc.MembershipPresent {
			t.Fatalf("Membership() = %q, want %q", got, ipc.MembershipPresent)
		}
		if got := plan.TimeoutMode(); got != ipc.TimeoutModeNative {
			t.Fatalf("TimeoutMode() = %q, want %q", got, ipc.TimeoutModeNative)
		}
		if expiry, found := plan.EffectiveUntilUnixMicro(); !found || expiry != 1700000000000000 {
			t.Fatalf("EffectiveUntilUnixMicro() = %d, %v, want 1700000000000000, true", expiry, found)
		}
		if got := plan.Scopes(); !reflect.DeepEqual(got, []ipc.Scope{ipc.ScopeInput}) {
			t.Fatalf("Scopes() = %#v, want [input]", got)
		}
	case "valid/remove-managed-infrastructure.json":
		remove, ok := request.(ipc.RemoveManagedInfrastructureRequest)
		if !ok {
			t.Fatalf("request type = %T, want ipc.RemoveManagedInfrastructureRequest", request)
		}
		if got := request.Operation(); got != ipc.OperationRemoveManagedInfrastructure {
			t.Fatalf("Operation() = %q, want %q", got, ipc.OperationRemoveManagedInfrastructure)
		}
		if got := remove.ExpectedOwnerVersion(); got != "guard/v1" {
			t.Fatalf("ExpectedOwnerVersion() = %q, want guard/v1", got)
		}
	default:
		t.Fatalf("unhandled mutation golden %q", path)
	}
}

func assertMutationRequestPlanBase(
	t *testing.T,
	request ipc.MutationRequest,
	plan ipc.ManagedPlan,
	wantDomain ipc.Domain,
	wantDigest string,
	wantRevision int64,
) {
	t.Helper()
	if got := request.Operation(); got != ipc.OperationApplyManagedPlan {
		t.Fatalf("Operation() = %q, want %q", got, ipc.OperationApplyManagedPlan)
	}
	if got := plan.Domain(); got != wantDomain {
		t.Fatalf("Domain() = %q, want %q", got, wantDomain)
	}
	if got := plan.OwnerVersion(); got != "guard/v1" {
		t.Fatalf("OwnerVersion() = %q, want guard/v1", got)
	}
	if got := plan.BasisSnapshotDigest(); got != wantDigest {
		t.Fatalf("BasisSnapshotDigest() = %q, want %q", got, wantDigest)
	}
	if got := plan.Revision(); got != wantRevision {
		t.Fatalf("Revision() = %d, want %d", got, wantRevision)
	}
}

func assertMutationRequestPrefixes(t *testing.T, got []netip.Prefix, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("prefix count = %d, want %d", len(got), len(want))
	}
	for index, prefix := range got {
		if value := prefix.String(); value != want[index] {
			t.Fatalf("prefix[%d] = %q, want %q", index, value, want[index])
		}
	}
}

func assertMutationRequestValidationError(t *testing.T, err error, want ipc.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("mutation request error = nil, want code %q", want)
	}
	var validationError *ipc.ValidationError
	if !errors.As(err, &validationError) {
		t.Fatalf("mutation request error type = %T, want *ipc.ValidationError", err)
	}
	if got := validationError.Code(); got != want {
		t.Fatalf("mutation request error code = %q, want %q (error: %v)", got, want, err)
	}
}

func compactMutationRequestGolden(t *testing.T, path string) []byte {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, readGoldenFile(t, path)); err != nil {
		t.Fatalf("compact golden %s: %v", path, err)
	}
	return compact.Bytes()
}

func mutationRequestPrefixes(count int) []netip.Prefix {
	prefixes := make([]netip.Prefix, count)
	for index := range prefixes {
		address := netip.AddrFrom4([4]byte{
			10,
			byte(index >> 16),
			byte(index >> 8),
			byte(index),
		})
		prefixes[index] = netip.PrefixFrom(address, 32)
	}
	sort.Slice(prefixes, func(left, right int) bool {
		return prefixes[left].String() < prefixes[right].String()
	})
	return prefixes
}
