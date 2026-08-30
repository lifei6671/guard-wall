// Package decision contains the pure Decision and projection rules used by the M0 fake slice.
package decision

import (
	"fmt"
	"net/netip"
	"time"

	"github.com/lifei6671/guard-wall/internal/core"
)

// AggregateProjection rebuilds one target projection from authoritative Decisions.
// Terminal Decisions are ignored. All Decisions must belong to the requested node and target.
func AggregateProjection(
	nodeID core.NodeID,
	target netip.Prefix,
	revision core.TargetProjectionRevision,
	decisions []core.Decision,
) (core.DesiredBanProjection, error) {
	if nodeID == "" {
		return core.DesiredBanProjection{}, fmt.Errorf("node id is required")
	}
	if !target.IsValid() || target != target.Masked() {
		return core.DesiredBanProjection{}, fmt.Errorf("target must be a canonical prefix")
	}

	projection := core.DesiredBanProjection{
		NodeID:          nodeID,
		CanonicalTarget: target,
		State:           core.BanProjectionAbsent,
		Revision:        revision,
	}
	var latestExpiry time.Time
	allFinite := true

	for _, current := range decisions {
		if err := current.Validate(); err != nil {
			return core.DesiredBanProjection{}, fmt.Errorf("validate decision %q: %w", current.ID, err)
		}
		if current.NodeID != nodeID || current.CanonicalTarget != target {
			return core.DesiredBanProjection{}, fmt.Errorf("decision %q belongs to another projection", current.ID)
		}
		if current.State != core.DecisionActive {
			continue
		}

		projection.ActiveCount++
		if current.ExpiresAt == nil {
			allFinite = false
			continue
		}
		if latestExpiry.IsZero() || current.ExpiresAt.After(latestExpiry) {
			latestExpiry = *current.ExpiresAt
		}
	}

	if projection.ActiveCount == 0 {
		if err := projection.Validate(); err != nil {
			return core.DesiredBanProjection{}, err
		}
		return projection, nil
	}
	projection.State = core.BanProjectionPresent
	if allFinite {
		expiry := latestExpiry
		projection.EffectiveUntil = &expiry
	}
	if err := projection.Validate(); err != nil {
		return core.DesiredBanProjection{}, err
	}
	return projection, nil
}
