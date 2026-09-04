//go:build linux

package nftables

import (
	"testing"
	"time"
)

func TestFixedDesiredInfrastructureMatchesNativeLayout(t *testing.T) {
	revision, desired := FixedDesiredInfrastructure()
	if revision != fixedInfrastructureRevision || !MatchesFixedDesiredInfrastructure(revision, desired) {
		t.Fatal("fixed desired Infrastructure did not match itself")
	}
	parsed, err := parseRuleset([]byte(managedInfrastructureRuleset()), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state, err := parsed.managedState()
	if err != nil {
		t.Fatal(err)
	}
	observed, present := state.Infrastructure()
	if !present || !MatchesFixedInfrastructureObservation(observed) {
		t.Fatal("fixed desired Infrastructure did not match the native layout observation")
	}
}

func TestFixedDesiredInfrastructureRejectsDrift(t *testing.T) {
	revision, desired := FixedDesiredInfrastructure()
	if MatchesFixedDesiredInfrastructure(revision+1, desired) {
		t.Fatal("fixed desired Infrastructure accepted another revision")
	}
	for _, mutate := range []func(){
		func() { desired.Backend = "fake" },
		func() { desired.OwnerVersion = "guard/v2" },
		func() { desired.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
	} {
		_, candidate := FixedDesiredInfrastructure()
		desired = candidate
		mutate()
		if MatchesFixedDesiredInfrastructure(revision, desired) {
			t.Fatal("fixed desired Infrastructure accepted a drifted identity")
		}
	}
}
