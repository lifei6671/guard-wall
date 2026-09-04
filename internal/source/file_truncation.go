package source

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
	"github.com/lifei6671/guard-wall/internal/store"
)

// FileObservation is one observation of an opened file, not a path lookup.
// ReadOffset is the caller's known offset; it may exceed Size after truncation.
type FileObservation struct {
	DeviceID   uint64
	Inode      uint64
	Size       uint64
	ReadOffset uint64
	ObservedAt time.Time
}

type FileObservationResult string

const (
	FileNoTruncationEvidence FileObservationResult = "no_truncation_evidence"
	FileIdentityChanged      FileObservationResult = "identity_changed"
	FileDataLossSuspected    FileObservationResult = "data_loss_suspected"
)

// FileTruncationHealth is process-local state, not restart recovery evidence.
type FileTruncationHealth struct {
	Degraded      bool
	StopReading   bool
	AuditRecorded bool
}

// FileTruncationObserver belongs to one generation and one serial read owner.
// Observe and Health must not run concurrently. It does not read or rotate files.
type FileTruncationObserver struct {
	nodeID     core.NodeID
	sourceID   core.SourceID
	generation string
	previous   FileObservation
	report     func(context.Context, store.SourceDataLossAudit) error
	pending    *store.SourceDataLossAudit
	health     FileTruncationHealth
}

func NewFileTruncationObserver(nodeID core.NodeID, sourceID core.SourceID, generation string, baseline FileObservation, report func(context.Context, store.SourceDataLossAudit) error) (*FileTruncationObserver, error) {
	if nodeID == "" || sourceID == "" || report == nil {
		return nil, fmt.Errorf("file observer identity and reporter are required")
	}
	if _, err := core.NewFilePosition(core.FilePosition{Generation: generation, DeviceID: baseline.DeviceID, Inode: baseline.Inode}); err != nil {
		return nil, err
	}
	if err := validateFileObservation(baseline); err != nil {
		return nil, err
	}
	return &FileTruncationObserver{nodeID: nodeID, sourceID: sourceID, generation: generation, previous: baseline, report: report}, nil
}

// Observe latches a visible loss before attempting synchronous Operational Audit.
// After detection, calls explicitly retry the original report until it succeeds;
// neither retry nor success clears StopReading. The owner must obey this result.
// No evidence does not exclude truncate-and-regrow between observations.
func (o *FileTruncationObserver) Observe(ctx context.Context, current FileObservation) (FileObservationResult, error) {
	if o == nil || o.report == nil || ctx == nil {
		return "", fmt.Errorf("initialized file observer and context are required")
	}
	if o.pending == nil {
		if err := validateFileObservation(current); err != nil {
			return "", err
		}
		if current.ObservedAt.Before(o.previous.ObservedAt) {
			return "", fmt.Errorf("file observation time regressed")
		}
		if current.DeviceID != o.previous.DeviceID || current.Inode != o.previous.Inode {
			return FileIdentityChanged, ctx.Err()
		}
		if current.Size >= o.previous.Size && current.Size >= current.ReadOffset {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			o.previous = current
			return FileNoTruncationEvidence, nil
		}
		o.pending = &store.SourceDataLossAudit{
			NodeID: o.nodeID, SourceID: o.sourceID, Generation: o.generation,
			DeviceID: current.DeviceID, Inode: current.Inode, PreviousSize: o.previous.Size,
			ReadOffset: current.ReadOffset, ObservedSize: current.Size, ObservedAt: current.ObservedAt,
		}
		// 报告失败也不能抹去已观察到的截断，或允许继续读取旧代际。
		o.health.Degraded, o.health.StopReading = true, true
	}
	if err := ctx.Err(); err != nil {
		return FileDataLossSuspected, err
	}
	if !o.health.AuditRecorded {
		if err := o.report(ctx, *o.pending); err != nil {
			return FileDataLossSuspected, err
		}
		o.health.AuditRecorded = true
	}
	return FileDataLossSuspected, nil
}

func (o *FileTruncationObserver) Health() FileTruncationHealth {
	if o == nil || o.report == nil {
		return FileTruncationHealth{StopReading: true}
	}
	return o.health
}

func validateFileObservation(observation FileObservation) error {
	if observation.DeviceID > math.MaxInt64 || observation.Inode > math.MaxInt64 || observation.Size > math.MaxInt64 || observation.ReadOffset > math.MaxInt64 || observation.ObservedAt.IsZero() || observation.ObservedAt.UnixMicro() <= 0 {
		return fmt.Errorf("file observation requires SQLite-range values and positive time")
	}
	return nil
}
