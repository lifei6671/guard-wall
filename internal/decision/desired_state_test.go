package decision

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
)

func TestWakeCommittedTargetsReportsFailedAndPendingSuffix(t *testing.T) {
	nodeID := core.NodeID("0123456789abcdef0123456789abcdef")
	changes := []TargetEnforcementChange{
		{NodeID: nodeID, Target: netip.MustParsePrefix("192.0.2.1/32")},
		{NodeID: nodeID, Target: netip.MustParsePrefix("192.0.2.2/32")},
		{NodeID: nodeID, Target: netip.MustParsePrefix("192.0.2.3/32")},
	}
	wakeFailure := errors.New("injected second wake failure")
	calls := 0
	err := WakeCommittedTargets(context.Background(), TargetWakeSinkFunc(
		func(context.Context, core.NodeID, netip.Prefix) error {
			calls++
			if calls == 2 {
				return wakeFailure
			}
			return nil
		},
	), changes)
	var wakeErr *PostCommitWakeError
	if !errors.As(err, &wakeErr) || !errors.Is(err, ErrPostCommitWake) ||
		!errors.Is(err, wakeFailure) {
		t.Fatalf("WakeCommittedTargets() error = %v", err)
	}
	if calls != 2 || wakeErr.Failed != changes[1] || len(wakeErr.Pending) != 1 ||
		wakeErr.Pending[0] != changes[2] {
		t.Fatalf("wake error = calls:%d failed:%+v pending:%+v", calls, wakeErr.Failed, wakeErr.Pending)
	}
}

func TestFunctionAdaptersRejectTypedNilWithoutPanicking(t *testing.T) {
	var policies TargetPolicyResolverFunc
	if _, err := policies.ResolveTargetPolicy(context.Background(), nil, core.DesiredBanProjection{}); err == nil {
		t.Fatal("typed-nil policy resolver error = nil")
	}
	var wake TargetWakeSinkFunc
	err := WakeCommittedTargets(context.Background(), wake, []TargetEnforcementChange{{
		NodeID: core.NodeID("0123456789abcdef0123456789abcdef"),
		Target: netip.MustParsePrefix("192.0.2.4/32"),
	}})
	if !errors.Is(err, ErrPostCommitWake) {
		t.Fatalf("typed-nil wake error = %v", err)
	}
}
