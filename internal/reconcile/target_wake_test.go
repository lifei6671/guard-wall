package reconcile

import (
	"context"
	"net/netip"
	"testing"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/firewall/fake"
)

func TestDispatcherTargetWakeSinkRoutesOnlyBoundNodeTarget(t *testing.T) {
	nodeID := core.NodeID("0123456789abcdef0123456789abcdef")
	dispatcher := &Dispatcher{
		queue: make(chan ReconcileKey, 1), done: make(chan struct{}),
		queued: make(map[ReconcileKey]*wakeReservation),
	}
	sink, err := NewDispatcherTargetWakeSink(nodeID, dispatcher)
	if err != nil {
		t.Fatal(err)
	}
	target := netip.MustParsePrefix("192.0.2.44/32")
	if err := sink.WakeTarget(context.Background(), nodeID, target); err != nil {
		t.Fatal(err)
	}
	select {
	case key := <-dispatcher.queue:
		if key != (ReconcileKey{Domain: fake.DomainTarget, Target: target}) {
			t.Fatalf("queued key = %+v", key)
		}
	default:
		t.Fatal("target wake was not queued")
	}
	if err := sink.WakeTarget(
		context.Background(), core.NodeID("fedcba9876543210fedcba9876543210"), target,
	); err == nil {
		t.Fatal("cross-node wake error = nil")
	}
}
