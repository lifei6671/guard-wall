package source

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/store"
)

func TestFileTruncationObserverReportsFirstEvidenceAndStops(t *testing.T) {
	base := FileObservation{DeviceID: 1, Inode: 2, Size: 20, ReadOffset: 10, ObservedAt: time.Unix(1700000000, 0).UTC()}
	for _, mode := range []string{"size-decrease", "offset-beyond-size", "cancelled-report"} {
		t.Run(mode, func(t *testing.T) {
			baseline := base
			current := base
			current.ObservedAt = base.ObservedAt.Add(time.Second)
			current.Size = 15
			if mode == "offset-beyond-size" {
				baseline.Size, current.Size, current.ReadOffset = 5, 5, 10
			}
			injected := errors.New("audit unavailable")
			var reports []store.SourceDataLossAudit
			observer, err := NewFileTruncationObserver("node", "source", "00112233445566778899aabbccddeeff", baseline, func(_ context.Context, event store.SourceDataLossAudit) error {
				reports = append(reports, event)
				if len(reports) == 1 && mode != "cancelled-report" {
					return injected
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			wantError := injected
			if mode == "cancelled-report" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, wantError = cancelled, context.Canceled
			}
			if result, err := observer.Observe(ctx, current); result != FileDataLossSuspected || !errors.Is(err, wantError) || observer.Health() != (FileTruncationHealth{Degraded: true, StopReading: true}) {
				t.Fatalf("failed report=%s/%v/%+v", result, err, observer.Health())
			}
			wanted := store.SourceDataLossAudit{NodeID: "node", SourceID: "source", Generation: "00112233445566778899aabbccddeeff", DeviceID: 1, Inode: 2, PreviousSize: baseline.Size, ReadOffset: current.ReadOffset, ObservedSize: current.Size, ObservedAt: current.ObservedAt}
			// 新观测不能覆盖第一次证据，报告成功后停止标记仍保持。
			later := current
			later.Size, later.Inode = 100, 3
			later.ObservedAt = current.ObservedAt.Add(time.Second)
			if result, err := observer.Observe(context.Background(), later); err != nil || result != FileDataLossSuspected || observer.Health() != (FileTruncationHealth{Degraded: true, StopReading: true, AuditRecorded: true}) {
				t.Fatalf("retry=%s/%v/%+v", result, err, observer.Health())
			}
			for _, event := range reports {
				if !reflect.DeepEqual(event, wanted) {
					t.Fatalf("event=%+v want=%+v", event, wanted)
				}
			}
			wantReports := 2
			if mode == "cancelled-report" {
				wantReports = 1
			}
			if len(reports) != wantReports {
				t.Fatalf("reports=%d want=%d", len(reports), wantReports)
			}
			count := len(reports)
			if _, err := observer.Observe(context.Background(), later); err != nil || len(reports) != count {
				t.Fatalf("duplicate report=%v count=%d", err, len(reports))
			}
		})
	}
}

func TestFileTruncationObserverNoEvidenceAndIdentityChange(t *testing.T) {
	base := FileObservation{DeviceID: 1, Inode: 2, Size: 20, ReadOffset: 10, ObservedAt: time.Unix(1700000000, 0).UTC()}
	observer, err := NewFileTruncationObserver("node", "source", "00112233445566778899aabbccddeeff", base, func(context.Context, store.SourceDataLossAudit) error { t.Fatal("unexpected report"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []uint64{20, 30} {
		current := base
		current.Size = size
		if result, err := observer.Observe(context.Background(), current); result != FileNoTruncationEvidence || err != nil {
			t.Fatalf("normal=%s/%v", result, err)
		}
	}
	changed := base
	changed.Inode, changed.Size = 3, 0
	if result, err := observer.Observe(context.Background(), changed); result != FileIdentityChanged || err != nil || observer.Health() != (FileTruncationHealth{}) {
		t.Fatalf("identity=%s/%v/%+v", result, err, observer.Health())
	}
	invalid := base
	invalid.ObservedAt = time.Time{}
	if _, err := observer.Observe(context.Background(), invalid); err == nil {
		t.Fatal("invalid observation accepted")
	}
}
