package detection

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

const (
	testNodeID     = core.NodeID("00112233445566778899aabbccddeeff")
	testGeneration = "00112233445566778899aabbccddeeff"
)

func TestLedgerAbortThenRetryContributesOnce(t *testing.T) {
	ledger := NewLedger()
	candidate := testCandidate(t, 0, "group-a", "distinct-a")

	reservation, previews, err := ledger.PrepareBatch(context.Background(), candidate.DeliveryID, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	assertPreviews(t, previews, []Preview{{Candidate: candidate, Snapshot: Snapshot{Count: 1, DistinctCount: 1}, WillContribute: true}})

	blockedContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := ledger.Snapshot(blockedContext, candidate.Window); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Snapshot() error = %v, want deadline while transaction is pending", err)
	}
	reservation.Abort()
	if got := mustSnapshot(t, ledger, candidate.Window); got != (Snapshot{}) {
		t.Fatalf("snapshot after abort = %+v, want zero", got)
	}

	retry, previews, err := ledger.PrepareBatch(context.Background(), candidate.DeliveryID, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 || !previews[0].WillContribute {
		t.Fatalf("retry preview = %+v, want one new contribution", previews)
	}
	retry.Confirm()
	retry.Confirm()
	retry.Abort()
	if got := mustSnapshot(t, ledger, candidate.Window); got != (Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("snapshot after retry = %+v, want count=1 distinct=1", got)
	}

	replay, previews, err := ledger.PrepareBatch(context.Background(), candidate.DeliveryID, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 || previews[0].WillContribute || previews[0].Snapshot.Count != 1 {
		t.Fatalf("replay preview = %+v, want committed duplicate", previews)
	}
	replay.Confirm()
	if got := mustSnapshot(t, ledger, candidate.Window); got.Count != 1 {
		t.Fatalf("snapshot after replay = %+v, want count=1", got)
	}
}

func TestZeroValueLedgerDefersAndResolvesEmptyAttempts(t *testing.T) {
	var ledger Ledger
	first := testCandidate(t, 40, "group-a", "distinct-a").DeliveryID
	reservation, previews, err := ledger.PrepareBatch(context.Background(), first, nil)
	if err != nil || len(previews) != 0 {
		t.Fatalf("PrepareBatch() = %v,%v", previews, err)
	}
	reservation.DeferResolution()
	if !ledger.ResolveDeferred(first, true) {
		t.Fatal("committed empty attempt was not resolved")
	}
	replay, _, err := ledger.PrepareBatch(context.Background(), first, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay.Abort()

	second := testCandidate(t, 41, "group-a", "distinct-a").DeliveryID
	reservation, _, err = ledger.PrepareBatch(context.Background(), second, nil)
	if err != nil {
		t.Fatal(err)
	}
	reservation.DeferResolution()
	if !ledger.ResolveDeferred(second, false) {
		t.Fatal("aborted empty attempt was not resolved")
	}
	retry, _, err := ledger.PrepareBatch(context.Background(), second, nil)
	if err != nil {
		t.Fatalf("aborted Delivery could not retry: %v", err)
	}
	retry.Abort()
}

func TestLedgerDistinctRefcountsAndDuplicateCandidate(t *testing.T) {
	ledger := NewLedger()
	first := testCandidate(t, 0, "group-a", "address-a")
	second := testCandidate(t, 1, "group-a", "address-a")
	third := testCandidate(t, 2, "group-a", "address-b")
	candidates := []Candidate{first, first, second, third}

	reservation, previews, err := ledger.PrepareBatch(context.Background(), first.DeliveryID, candidatesForDelivery(t, first.DeliveryID, candidates))
	if err != nil {
		t.Fatal(err)
	}
	want := []Snapshot{
		{Count: 1, DistinctCount: 1},
		{Count: 1, DistinctCount: 1},
		{Count: 2, DistinctCount: 1},
		{Count: 3, DistinctCount: 2},
	}
	for index, preview := range previews {
		if preview.Snapshot != want[index] {
			t.Fatalf("preview %d = %+v, want %+v", index, preview.Snapshot, want[index])
		}
		if preview.WillContribute != (index != 1) {
			t.Fatalf("preview %d WillContribute = %v", index, preview.WillContribute)
		}
	}
	reservation.Confirm()
	if got := mustSnapshot(t, ledger, first.Window); got != (Snapshot{Count: 3, DistinctCount: 2}) {
		t.Fatalf("snapshot = %+v, want count=3 distinct=2", got)
	}
}

func TestLedgerRejectsMembershipMappedToAnotherWindow(t *testing.T) {
	ledger := NewLedger()
	candidate := testCandidate(t, 0, "group-a", "address-a")
	reservation, _, err := ledger.PrepareBatch(context.Background(), candidate.DeliveryID, []Candidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	reservation.Confirm()

	conflicting := candidate
	conflicting.Window.GroupKey = "group-b"
	if _, _, err := ledger.PrepareBatch(context.Background(), candidate.DeliveryID, []Candidate{conflicting}); err == nil {
		t.Fatal("PrepareBatch() accepted one membership mapped to another Window")
	}
	if got := mustSnapshot(t, ledger, candidate.Window); got.Count != 1 {
		t.Fatalf("original Window snapshot = %+v, want count=1", got)
	}
}

func TestLedgerMultiGroupReverseOrderDoesNotDeadlock(t *testing.T) {
	ledger := NewLedger()
	firstA := testCandidate(t, 0, "group-a", "a")
	firstB := testCandidate(t, 1, "group-b", "b")
	secondA := testCandidate(t, 2, "group-a", "c")
	secondB := testCandidate(t, 3, "group-b", "d")

	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var workers sync.WaitGroup
	for _, test := range []struct {
		delivery   core.DeliveryID
		candidates []Candidate
	}{
		{firstA.DeliveryID, candidatesForDelivery(t, firstA.DeliveryID, []Candidate{firstB, firstA})},
		{secondA.DeliveryID, candidatesForDelivery(t, secondA.DeliveryID, []Candidate{secondA, secondB})},
	} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reservation, _, err := ledger.PrepareBatch(ctx, test.delivery, test.candidates)
			if err == nil {
				reservation.Confirm()
			}
			errorsChannel <- err
		}()
	}
	close(start)
	workers.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent PrepareBatch() error = %v", err)
		}
	}
	if got := mustSnapshot(t, ledger, firstA.Window); got.Count != 2 {
		t.Fatalf("group-a snapshot = %+v, want count=2", got)
	}
	if got := mustSnapshot(t, ledger, firstB.Window); got.Count != 2 {
		t.Fatalf("group-b snapshot = %+v, want count=2", got)
	}
}

func TestLedgerSameGroupIsSerializedAndCancellable(t *testing.T) {
	ledger := NewLedger()
	first := testCandidate(t, 0, "group-a", "a")
	second := testCandidate(t, 1, "group-a", "b")

	held, _, err := ledger.PrepareBatch(context.Background(), first.DeliveryID, []Candidate{first})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := ledger.PrepareBatch(ctx, second.DeliveryID, []Candidate{second}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked PrepareBatch() error = %v, want deadline", err)
	}
	held.Confirm()

	next, previews, err := ledger.PrepareBatch(context.Background(), second.DeliveryID, []Candidate{second})
	if err != nil {
		t.Fatal(err)
	}
	if len(previews) != 1 || previews[0].Snapshot.Count != 2 {
		t.Fatalf("serialized preview = %+v, want count=2", previews)
	}
	next.Confirm()
}

func TestLedgerAbortAndConfirmAreIdempotent(t *testing.T) {
	ledger := NewLedger()
	abortedCandidate := testCandidate(t, 0, "group-a", "a")
	aborted, _, err := ledger.PrepareBatch(context.Background(), abortedCandidate.DeliveryID, []Candidate{abortedCandidate})
	if err != nil {
		t.Fatal(err)
	}
	aborted.Abort()
	aborted.Abort()
	aborted.Confirm()
	if got := mustSnapshot(t, ledger, abortedCandidate.Window); got != (Snapshot{}) {
		t.Fatalf("snapshot after idempotent Abort = %+v, want zero", got)
	}

	confirmedCandidate := testCandidate(t, 1, "group-a", "b")
	confirmed, _, err := ledger.PrepareBatch(context.Background(), confirmedCandidate.DeliveryID, []Candidate{confirmedCandidate})
	if err != nil {
		t.Fatal(err)
	}
	confirmed.Confirm()
	confirmed.Confirm()
	confirmed.Abort()
	if got := mustSnapshot(t, ledger, confirmedCandidate.Window); got != (Snapshot{Count: 1, DistinctCount: 1}) {
		t.Fatalf("snapshot after idempotent Confirm = %+v, want count=1 distinct=1", got)
	}
}

func testCandidate(t *testing.T, index uint64, group, distinct string) Candidate {
	t.Helper()
	position := core.FilePosition{
		Generation:  testGeneration,
		StartOffset: index * 10,
		EndOffset:   index*10 + 10,
	}
	deliveryID, err := core.FileDeliveryID("source-1", position)
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := core.SecurityEventID(testNodeID, deliveryID, "parser-1", "v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	return Candidate{
		DeliveryID: deliveryID,
		Key: ContributionKey{
			EventID: eventID, RuleID: "rule-1", RuleVersion: "v1",
		},
		Window:      WindowKey{RuleID: "rule-1", RuleVersion: "v1", GroupKey: group},
		DistinctKey: distinct,
		ObservedAt:  time.Unix(100+int64(index), 0).UTC(),
	}
}

func candidatesForDelivery(t *testing.T, deliveryID core.DeliveryID, candidates []Candidate) []Candidate {
	t.Helper()
	result := make([]Candidate, len(candidates))
	eventIDs := make(map[core.EventID]core.EventID, len(candidates))
	for index, candidate := range candidates {
		candidate.DeliveryID = deliveryID
		eventID, found := eventIDs[candidate.Key.EventID]
		if !found {
			var err error
			eventID, err = core.SecurityEventID(testNodeID, deliveryID, "parser-1", "v1", uint32(index))
			if err != nil {
				t.Fatal(err)
			}
			eventIDs[candidate.Key.EventID] = eventID
		}
		candidate.Key.EventID = eventID
		result[index] = candidate
	}
	return result
}

func mustSnapshot(t *testing.T, ledger *Ledger, key WindowKey) Snapshot {
	t.Helper()
	snapshot, err := ledger.Snapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertPreviews(t *testing.T, got, want []Preview) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("preview count = %d, want %d", len(got), len(want))
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("preview %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}
