//go:build linux

// Package nftables implements Guard's fixed nftables-native mutation boundary.
package nftables

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
)

const (
	nftBinary             = "nft"
	managedFamily         = "inet"
	managedTable          = "guard"
	infrastructureComment = "guard/v1 infrastructure/v1"
	policyComment         = "guard/v1 policy/v1"
	infrastructureV       = "guard-nftables-infrastructure/v1"
)

var (
	errNftUnsupported = errors.New("nftables-native is unavailable")
	errNftNotReady    = errors.New("nftables-native is not ready")
	errNftOwnership   = errors.New("nftables-native ownership is unproven")
)

var managedSets = []string{
	"allow_v4", "allow_v6", "protected_v4", "protected_v6",
	"ban_input_v4", "ban_input_v6", "ban_forward_v4", "ban_forward_v6",
}

var managedChains = []string{"input", "forward", "guard_policy"}

type commandRunner interface {
	run(context.Context, []byte, ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) run(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, nftBinary, args...)
	command.Stdin = strings.NewReader(string(input))
	return command.Output()
}

// Backend executes only the fixed Guard-owned nftables layout. It must not be
// copied after first use.
type Backend struct{ runner commandRunner }

// NewBackend constructs a native nftables backend with the fixed system nft
// binary. Callers cannot select a binary, table, chain, or set.
func NewBackend() *Backend { return &Backend{runner: execRunner{}} }

func newBackend(runner commandRunner) *Backend { return &Backend{runner: runner} }

// Probe reports capability only when the fixed nftables binary and current
// ruleset can be read. Known UFW/Docker ownership makes mutation fail closed.
func (b *Backend) Probe(ctx context.Context) (firewall.FirewallCapabilities, error) {
	if b == nil || b.runner == nil || ctx == nil {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendNotReady
	}
	version, err := b.runner.run(ctx, nil, "--version")
	if err != nil || ctx.Err() != nil {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendUnsupported
	}
	toolVersion := strings.TrimSpace(string(version))
	raw, err := b.runner.run(ctx, nil, "--json", "list", "ruleset")
	if err != nil || ctx.Err() != nil {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendNotReady
	}
	state, err := parseRuleset(raw, time.Now())
	if err != nil {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendNotReady
	}
	if state.ownershipConflict {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendOwnershipConflict
	}
	coexisting := !state.hasUFW && !state.hasDocker && !state.hasUnknownPacketPath
	capabilities, err := firewall.NewFirewallCapabilities(firewall.FirewallCapabilitiesSpec{
		Backend: firewall.BackendKindNftablesNative, ToolVersion: toolVersion,
		IPv4: true, IPv6: true, CIDR: true, NativeSet: true, NativeTimeout: true,
		CrashSafeExpiry: true, AtomicBatch: true, HostInput: true, Forward: true,
		UFWIntegrationProven: coexisting, DockerIntegrationProven: coexisting,
		OwnershipProven: true, MutationReady: coexisting,
	})
	if err != nil {
		return firewall.FirewallCapabilities{}, enforcer.ErrMutationBackendNotReady
	}
	return capabilities, nil
}

// Snapshot reads all nftables state once and returns only Guard's typed state
// plus a digest for everything outside Guard's fixed private table.
func (b *Backend) Snapshot(ctx context.Context) (firewall.ManagedSnapshot, error) {
	if b == nil || b.runner == nil || ctx == nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	raw, err := b.runner.run(ctx, nil, "--json", "list", "ruleset")
	if err != nil || ctx.Err() != nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	state, err := parseRuleset(raw, time.Now())
	if err != nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	if state.ownershipConflict {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendOwnershipConflict
	}
	foreign, err := firewall.NewForeignContext(firewall.ForeignContextSpec{Digest: state.foreignDigest})
	if err != nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	managed, err := state.managedState()
	if err != nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	snapshot, err := firewall.NewManagedSnapshot(firewall.ManagedSnapshotSpec{ManagedState: managed, ForeignContext: foreign})
	if err != nil {
		return firewall.ManagedSnapshot{}, enforcer.ErrMutationBackendNotReady
	}
	return snapshot, nil
}

// Apply re-observes immediately before its sole fixed nft batch. A failure
// after dispatch is Unknown because the post-state is not proven.
func (b *Backend) Apply(ctx context.Context, plan firewall.OperationPlan) firewall.MutationResult {
	if !validOperationPlan(plan) || ctx == nil || ctx.Err() != nil {
		return rejected(plan, firewall.MutationErrorInvalidPlan)
	}
	capabilities, err := b.Probe(ctx)
	if err != nil || !reflect.DeepEqual(capabilities, plan.Capabilities()) {
		return rejected(plan, firewall.MutationErrorNotReady)
	}
	snapshot, err := b.Snapshot(ctx)
	if err != nil || snapshot.Digest() != plan.BasisSnapshotDigest() {
		return rejected(plan, firewall.MutationErrorNotReady)
	}
	state := snapshot.ManagedState()
	var batch string
	switch value := plan.(type) {
	case firewall.InfrastructureOperationPlan:
		if _, present := state.Infrastructure(); present {
			return confirmed(plan)
		}
		batch = infrastructureBatch()
	case firewall.PolicyOperationPlan:
		if _, present := state.Infrastructure(); !present {
			return rejected(plan, firewall.MutationErrorNotReady)
		}
		batch = policyBatch(value)
	case firewall.TargetOperationPlan:
		if value.Membership() == firewall.TargetMembershipAbsent && !targetPresent(state.Targets(), value.Target()) {
			return confirmed(plan)
		}
		batch = targetBatch(value, state.Targets())
	default:
		return rejected(plan, firewall.MutationErrorInvalidPlan)
	}
	if batch == "" {
		return rejected(plan, firewall.MutationErrorInvalidPlan)
	}
	if _, err := b.runner.run(ctx, []byte(batch), "--file", "-"); err != nil || ctx.Err() != nil {
		return unknown(plan)
	}
	after, err := b.Snapshot(ctx)
	if err != nil || after.ForeignContext().Digest() != snapshot.ForeignContext().Digest() || !applyPostcondition(after.ManagedState(), plan) {
		return unknown(plan)
	}
	return confirmed(plan)
}

// RemoveManagedInfrastructure removes only the fixed owned table after a fresh
// exact ownership and basis check.
func (b *Backend) RemoveManagedInfrastructure(ctx context.Context, authorization firewall.RemovalAuthorization) firewall.MutationResult {
	if !validRemoval(authorization) || ctx == nil || ctx.Err() != nil {
		return rejected(authorization, firewall.MutationErrorOwnershipConflict)
	}
	capabilities, err := b.Probe(ctx)
	if err != nil || !reflect.DeepEqual(capabilities, authorization.Capabilities()) {
		return rejected(authorization, firewall.MutationErrorNotReady)
	}
	snapshot, err := b.Snapshot(ctx)
	if err != nil || snapshot.Digest() != authorization.BasisSnapshotDigest() {
		return rejected(authorization, firewall.MutationErrorNotReady)
	}
	if _, present := snapshot.ManagedState().Infrastructure(); !present {
		return confirmed(authorization)
	}
	if _, err := b.runner.run(ctx, []byte("delete table inet guard\n"), "--file", "-"); err != nil || ctx.Err() != nil {
		return unknown(authorization)
	}
	after, err := b.Snapshot(ctx)
	if err != nil || after.ForeignContext().Digest() != snapshot.ForeignContext().Digest() || hasInfrastructure(after.ManagedState()) {
		return unknown(authorization)
	}
	return confirmed(authorization)
}

func applyPostcondition(state firewall.ManagedState, plan firewall.OperationPlan) bool {
	switch value := plan.(type) {
	case firewall.InfrastructureOperationPlan:
		return hasInfrastructure(state)
	case firewall.PolicyOperationPlan:
		policy, present := state.Policy()
		return present && policy.RelationDigest() == policyDigest(policyElements(value))
	case firewall.TargetOperationPlan:
		for _, observed := range state.Targets() {
			if observed.Target() != value.Target() {
				continue
			}
			if value.Membership() == firewall.TargetMembershipAbsent {
				return false
			}
			if observed.TimeoutMode() != value.TimeoutMode() || !reflect.DeepEqual(observed.Scopes(), value.Scopes()) {
				return false
			}
			until, wantsExpiry := value.EffectiveUntilUnixMicro()
			got, hasExpiry := observed.EffectiveUntilUnixMicro()
			return !wantsExpiry || hasExpiry && got > time.Now().UnixMicro() && got <= until
		}
		return value.Membership() == firewall.TargetMembershipAbsent
	default:
		return false
	}
}

func hasInfrastructure(state firewall.ManagedState) bool {
	_, present := state.Infrastructure()
	return present
}

func validOperationPlan(plan firewall.OperationPlan) bool {
	if plan == nil || typedNil(plan) || plan.Backend() != firewall.BackendKindNftablesNative ||
		plan.OwnerVersion() != firewall.ManagedOwnerVersionV1 || plan.Digest() == "" || plan.Revision() <= 0 {
		return false
	}
	capabilities := plan.Capabilities()
	return capabilities.Validate() == nil && capabilities.MutationReady() && capabilities.OwnershipProven()
}

func validRemoval(authorization firewall.RemovalAuthorization) bool {
	if authorization == nil || typedNil(authorization) || authorization.Backend() != firewall.BackendKindNftablesNative ||
		authorization.ExpectedOwnerVersion() != firewall.ManagedOwnerVersionV1 || authorization.Digest() == "" {
		return false
	}
	capabilities := authorization.Capabilities()
	return capabilities.Validate() == nil && capabilities.MutationReady() && capabilities.OwnershipProven()
}

func typedNil(value any) bool {
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func confirmed(mutation firewall.AuthorizedMutation) firewall.MutationResult {
	result, _ := firewall.NewConfirmedMutationResult(mutation)
	return result
}

func rejected(mutation firewall.AuthorizedMutation, code firewall.MutationErrorCode) firewall.MutationResult {
	result, _ := firewall.NewRejectedMutationResult(mutation, code)
	return result
}

func unknown(mutation firewall.AuthorizedMutation) firewall.MutationResult {
	result, _ := firewall.NewUnknownMutationResult(mutation)
	return result
}

func infrastructureBatch() string {
	return `add table inet guard
add set inet guard allow_v4 { type ipv4_addr; flags interval; }
add set inet guard allow_v6 { type ipv6_addr; flags interval; }
add set inet guard protected_v4 { type ipv4_addr; flags interval; }
add set inet guard protected_v6 { type ipv6_addr; flags interval; }
add set inet guard ban_input_v4 { type ipv4_addr; flags interval,timeout; }
add set inet guard ban_input_v6 { type ipv6_addr; flags interval,timeout; }
add set inet guard ban_forward_v4 { type ipv4_addr; flags interval,timeout; }
add set inet guard ban_forward_v6 { type ipv6_addr; flags interval,timeout; }
add chain inet guard guard_policy
add rule inet guard guard_policy counter comment "guard/v1 infrastructure/v1"
add chain inet guard input { type filter hook input priority 0; policy accept; }
add chain inet guard forward { type filter hook forward priority 0; policy accept; }
add rule inet guard input ip saddr @allow_v4 return
add rule inet guard input ip saddr @protected_v4 return
add rule inet guard input ip saddr @ban_input_v4 drop
add rule inet guard input ip6 saddr @allow_v6 return
add rule inet guard input ip6 saddr @protected_v6 return
add rule inet guard input ip6 saddr @ban_input_v6 drop
add rule inet guard forward ip saddr @allow_v4 return
add rule inet guard forward ip saddr @protected_v4 return
add rule inet guard forward ip saddr @ban_forward_v4 drop
add rule inet guard forward ip6 saddr @allow_v6 return
add rule inet guard forward ip6 saddr @protected_v6 return
add rule inet guard forward ip6 saddr @ban_forward_v6 drop
`
}

func policyBatch(plan firewall.PolicyOperationPlan) string {
	var builder strings.Builder
	builder.WriteString("flush set inet guard allow_v4\nflush set inet guard allow_v6\nflush set inet guard protected_v4\nflush set inet guard protected_v6\nflush chain inet guard guard_policy\n")
	writeElements(&builder, "allow_v4", ipv4(plan.Allowlist()), "")
	writeElements(&builder, "allow_v6", ipv6(plan.Allowlist()), "")
	writeElements(&builder, "protected_v4", ipv4(plan.ProtectedTargets()), "")
	writeElements(&builder, "protected_v6", ipv6(plan.ProtectedTargets()), "")
	builder.WriteString("add rule inet guard guard_policy counter comment \"guard/v1 infrastructure/v1\"\n")
	builder.WriteString("add rule inet guard guard_policy counter comment \"guard/v1 policy/v1\"\n")
	return builder.String()
}

func policyElements(plan firewall.PolicyOperationPlan) map[string][]element {
	sets := make(map[string][]element)
	for _, entry := range []struct {
		name     string
		prefixes []netip.Prefix
	}{
		{"allow_v4", ipv4(plan.Allowlist())}, {"allow_v6", ipv6(plan.Allowlist())},
		{"protected_v4", ipv4(plan.ProtectedTargets())}, {"protected_v6", ipv6(plan.ProtectedTargets())},
	} {
		for _, prefix := range entry.prefixes {
			sets[entry.name] = append(sets[entry.name], element{prefix: prefix})
		}
	}
	return sets
}

func targetBatch(plan firewall.TargetOperationPlan, observed []firewall.TargetObservation) string {
	sets := targetSets(plan.Target(), plan.Scopes())
	priorScopes := scopesForTarget(observed, plan.Target())
	if plan.Membership() == firewall.TargetMembershipAbsent {
		sets = targetSets(plan.Target(), priorScopes)
	}
	if len(sets) == 0 {
		return ""
	}
	var builder strings.Builder
	if plan.Membership() == firewall.TargetMembershipPresent {
		for _, set := range targetSets(plan.Target(), priorScopes) {
			fmt.Fprintf(&builder, "delete element inet guard %s { %s }\n", set, plan.Target())
		}
	}
	for _, set := range sets {
		if plan.Membership() == firewall.TargetMembershipAbsent {
			fmt.Fprintf(&builder, "delete element inet guard %s { %s }\n", set, plan.Target())
			continue
		}
		timeout := ""
		if until, ok := plan.EffectiveUntilUnixMicro(); ok {
			remaining := time.Until(time.UnixMicro(until))
			if remaining <= 0 {
				return ""
			}
			milliseconds := remaining.Milliseconds()
			if milliseconds < 1 {
				milliseconds = 1
			}
			timeout = fmt.Sprintf(" timeout %dms", milliseconds)
		}
		fmt.Fprintf(&builder, "add element inet guard %s { %s%s }\n", set, plan.Target(), timeout)
	}
	return builder.String()
}

func writeElements(builder *strings.Builder, set string, prefixes []netip.Prefix, suffix string) {
	if len(prefixes) == 0 {
		return
	}
	builder.WriteString("add element inet guard ")
	builder.WriteString(set)
	builder.WriteString(" { ")
	for index, prefix := range prefixes {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(prefix.String())
	}
	builder.WriteString(suffix)
	builder.WriteString(" }\n")
}

func ipv4(prefixes []netip.Prefix) []netip.Prefix { return family(prefixes, true) }
func ipv6(prefixes []netip.Prefix) []netip.Prefix { return family(prefixes, false) }
func family(prefixes []netip.Prefix, v4 bool) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(prefixes))
	for _, prefix := range prefixes {
		if prefix.Addr().Is4() == v4 {
			result = append(result, prefix)
		}
	}
	return result
}

func targetSets(target netip.Prefix, scopes []firewall.ManagedScope) []string {
	family := "v6"
	if target.Addr().Is4() {
		family = "v4"
	}
	sets := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		switch scope {
		case firewall.ManagedScopeInput:
			sets = append(sets, "ban_input_"+family)
		case firewall.ManagedScopeForward:
			sets = append(sets, "ban_forward_"+family)
		}
	}
	return sets
}

func targetPresent(targets []firewall.TargetObservation, target netip.Prefix) bool {
	for _, observed := range targets {
		if observed.Target() == target {
			return true
		}
	}
	return false
}

func scopesForTarget(targets []firewall.TargetObservation, target netip.Prefix) []firewall.ManagedScope {
	for _, observed := range targets {
		if observed.Target() == target {
			return observed.Scopes()
		}
	}
	return nil
}

type rulesetState struct {
	managed              bool
	strictLayout         bool
	infrastructureMarker bool
	ownershipConflict    bool
	policy               bool
	hasUFW               bool
	hasDocker            bool
	hasUnknownPacketPath bool
	sets                 map[string][]element
	rules                map[string]int
	ruleSequence         map[string][]string
	markers              map[string]int
	foreignDigest        string
}

type element struct {
	prefix  netip.Prefix
	expires *int64
}

func parseRuleset(raw []byte, now time.Time) (rulesetState, error) {
	var document struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &document); err != nil || document.Nftables == nil {
		return rulesetState{}, errNftNotReady
	}
	state := rulesetState{sets: make(map[string][]element), rules: make(map[string]int), ruleSequence: make(map[string][]string), markers: make(map[string]int)}
	filtered := make([]map[string]json.RawMessage, 0, len(document.Nftables))
	objects := make(map[string]bool)
	for _, entry := range document.Nftables {
		kind, body, ok := nftObject(entry)
		if !ok {
			return rulesetState{}, errNftNotReady
		}
		head := nftHead(body)
		if kind == "table" && head.family == managedFamily && head.name == managedTable {
			state.managed = true
			state.strictLayout = head.handle > 0
			continue
		}
		if head.family == managedFamily && head.table == managedTable {
			if !state.managed {
				state.ownershipConflict = true
			}
			switch kind {
			case "set":
				objects[kind+":"+head.name] = true
				if state.strictLayout && !validManagedSet(head, body) {
					state.ownershipConflict = true
				}
				elements, ok := nftSetElements(body, now)
				if !ok {
					state.ownershipConflict = true
					continue
				}
				state.sets[head.name] = append(state.sets[head.name], elements...)
			case "chain":
				objects[kind+":"+head.name] = true
				if state.strictLayout && !validManagedChain(head) {
					state.ownershipConflict = true
				}
			case "rule":
				if state.strictLayout && !validManagedRule(head, body) {
					state.ownershipConflict = true
				}
				state.rules[head.chain]++
				if state.strictLayout && (head.chain == "input" || head.chain == "forward") {
					set, verdict, ok := expectedRule(head.chain, body)
					if !ok {
						state.ownershipConflict = true
					} else {
						state.ruleSequence[head.chain] = append(state.ruleSequence[head.chain], set+":"+verdict)
					}
				}
				if head.chain == "guard_policy" {
					state.markers[head.comment]++
					switch head.comment {
					case infrastructureComment:
						state.infrastructureMarker = true
					case policyComment:
						state.policy = true
					}
				}
			case "element", "elem", "setelem":
				prefix, expires, ok := nftElement(body, now)
				if !ok {
					state.ownershipConflict = true
					continue
				}
				state.sets[head.name] = append(state.sets[head.name], element{prefix: prefix, expires: expires})
			}
			continue
		}
		switch knownManager(head) {
		case "ufw":
			state.hasUFW = true
		case "docker":
			state.hasDocker = true
		}
		if isForeignPacketPath(kind, head) {
			state.hasUnknownPacketPath = true
		}
		filtered = append(filtered, entry)
	}
	if state.managed {
		if !state.infrastructureMarker {
			state.ownershipConflict = true
		}
		for _, set := range managedSets {
			if !objects["set:"+set] {
				state.ownershipConflict = true
			}
		}
		for _, chain := range managedChains {
			if !objects["chain:"+chain] {
				state.ownershipConflict = true
			}
		}
		for object := range objects {
			kind, name, _ := strings.Cut(object, ":")
			if kind == "set" && !contains(managedSets, name) || kind == "chain" && !contains(managedChains, name) {
				state.ownershipConflict = true
			}
		}
		if state.strictLayout && !validManagedRuleCounts(state.rules, state.ruleSequence, state.markers) {
			state.ownershipConflict = true
		}
	}
	foreignDigest, err := canonicalForeignDigest(filtered)
	if err != nil {
		return rulesetState{}, err
	}
	state.foreignDigest = foreignDigest
	return state, nil
}

func canonicalForeignDigest(entries []map[string]json.RawMessage) (string, error) {
	unordered := make([]json.RawMessage, 0, len(entries))
	rulesByChain := make(map[string][]json.RawMessage)
	for _, entry := range entries {
		kind, body, ok := nftObject(entry)
		if !ok {
			return "", errNftNotReady
		}
		canonicalBody, err := canonicalNftJSON(body, false, kind == "set" || kind == "map" || kind == "element" || kind == "elem" || kind == "setelem")
		if err != nil {
			return "", err
		}
		canonical, err := json.Marshal(map[string]json.RawMessage{kind: canonicalBody})
		if err != nil {
			return "", err
		}
		if kind == "rule" {
			head := nftHead(body)
			key := head.family + "\x00" + head.table + "\x00" + head.chain
			rulesByChain[key] = append(rulesByChain[key], canonical)
			continue
		}
		unordered = append(unordered, canonical)
	}
	sort.Slice(unordered, func(i, j int) bool { return bytes.Compare(unordered[i], unordered[j]) < 0 })
	ruleChains := make([]string, 0, len(rulesByChain))
	for chain := range rulesByChain {
		ruleChains = append(ruleChains, chain)
	}
	sort.Strings(ruleChains)
	canonical := make([]json.RawMessage, 0, len(entries))
	canonical = append(canonical, unordered...)
	for _, chain := range ruleChains {
		canonical = append(canonical, rulesByChain[chain]...)
	}
	encoded, err := json.Marshal(struct {
		Nftables []json.RawMessage `json:"nftables"`
	}{canonical})
	if err != nil {
		return "", err
	}
	return sha256hex(encoded), nil
}

func canonicalNftJSON(raw json.RawMessage, counter, sortElements bool) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, errNftNotReady
	}
	switch raw[0] {
	case '{':
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return nil, err
		}
		canonical := make(map[string]json.RawMessage, len(object))
		for key, value := range object {
			if key == "handle" || counter && (key == "packets" || key == "bytes") {
				continue
			}
			normalized, err := canonicalNftJSON(value, key == "counter", sortElements && key == "elem")
			if err != nil {
				return nil, err
			}
			canonical[key] = normalized
		}
		return json.Marshal(canonical)
	case '[':
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return nil, err
		}
		canonical := make([]json.RawMessage, len(array))
		for index, value := range array {
			normalized, err := canonicalNftJSON(value, false, false)
			if err != nil {
				return nil, err
			}
			canonical[index] = normalized
		}
		if sortElements {
			sort.Slice(canonical, func(i, j int) bool { return bytes.Compare(canonical[i], canonical[j]) < 0 })
		}
		return json.Marshal(canonical)
	default:
		var compact bytes.Buffer
		if err := json.Compact(&compact, raw); err != nil {
			return nil, err
		}
		return compact.Bytes(), nil
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type nftHeader struct {
	family, table, name, chain, comment string
	chainType, hook, policy             string
	priority                            int
	handle                              int
}

func nftObject(entry map[string]json.RawMessage) (string, json.RawMessage, bool) {
	if len(entry) != 1 {
		return "", nil, false
	}
	for kind, body := range entry {
		return kind, body, true
	}
	return "", nil, false
}

func nftHead(body json.RawMessage) nftHeader {
	var value struct {
		Family  string `json:"family"`
		Table   string `json:"table"`
		Name    string `json:"name"`
		Chain   string `json:"chain"`
		Comment string `json:"comment"`
		Type    string `json:"type"`
		Hook    string `json:"hook"`
		Prio    int    `json:"prio"`
		Policy  string `json:"policy"`
		Handle  int    `json:"handle"`
	}
	_ = json.Unmarshal(body, &value)
	return nftHeader{family: value.Family, table: value.Table, name: value.Name, chain: value.Chain, comment: value.Comment, chainType: value.Type, hook: value.Hook, priority: value.Prio, policy: value.Policy, handle: value.Handle}
}

func validManagedSet(head nftHeader, body json.RawMessage) bool {
	wantsTimeout := strings.HasPrefix(head.name, "ban_")
	wantsV4 := strings.HasSuffix(head.name, "_v4")
	var value struct {
		Type  string   `json:"type"`
		Flags []string `json:"flags"`
	}
	if json.Unmarshal(body, &value) != nil || value.Type != map[bool]string{true: "ipv4_addr", false: "ipv6_addr"}[wantsV4] {
		return false
	}
	wanted := []string{"interval"}
	if wantsTimeout {
		wanted = append(wanted, "timeout")
	}
	return reflect.DeepEqual(value.Flags, wanted)
}

func validManagedChain(head nftHeader) bool {
	switch head.name {
	case "guard_policy":
		return head.chainType == "" && head.hook == "" && head.policy == ""
	case "input", "forward":
		return head.chainType == "filter" && head.hook == head.name && head.priority == 0 && head.policy == "accept"
	default:
		return false
	}
}

func validManagedRule(head nftHeader, body json.RawMessage) bool {
	if head.chain == "guard_policy" {
		if head.comment != infrastructureComment && head.comment != policyComment {
			return false
		}
		var value struct {
			Expr []map[string]json.RawMessage `json:"expr"`
		}
		return json.Unmarshal(body, &value) == nil && len(value.Expr) == 1 && value.Expr[0]["counter"] != nil
	}
	set, verdict, ok := expectedRule(head.chain, body)
	return ok && set != "" && verdict != ""
}

func expectedRule(chain string, body json.RawMessage) (string, string, bool) {
	var value struct {
		Expr []json.RawMessage `json:"expr"`
	}
	if json.Unmarshal(body, &value) != nil || len(value.Expr) != 2 {
		return "", "", false
	}
	var first struct {
		Match *struct {
			Op   string `json:"op"`
			Left struct {
				Payload struct {
					Protocol string `json:"protocol"`
					Field    string `json:"field"`
				} `json:"payload"`
			} `json:"left"`
			Right string `json:"right"`
		} `json:"match"`
	}
	if json.Unmarshal(value.Expr[0], &first) != nil || first.Match == nil {
		return "", "", false
	}
	m := first.Match
	if m.Op != "==" || m.Left.Payload.Field != "saddr" || (m.Left.Payload.Protocol != "ip" && m.Left.Payload.Protocol != "ip6") || !strings.HasPrefix(m.Right, "@") {
		return "", "", false
	}
	verdict := ""
	var second map[string]json.RawMessage
	if json.Unmarshal(value.Expr[1], &second) != nil {
		return "", "", false
	}
	if _, ok := second["return"]; ok {
		verdict = "return"
	}
	if _, ok := second["drop"]; ok {
		verdict = "drop"
	}
	name := strings.TrimPrefix(m.Right, "@")
	if (strings.HasSuffix(name, "_v4") && m.Left.Payload.Protocol != "ip") || (strings.HasSuffix(name, "_v6") && m.Left.Payload.Protocol != "ip6") {
		return "", "", false
	}
	expected := map[string]string{"allow_v4": "return", "allow_v6": "return", "protected_v4": "return", "protected_v6": "return", "ban_input_v4": "drop", "ban_input_v6": "drop", "ban_forward_v4": "drop", "ban_forward_v6": "drop"}
	if expected[name] != verdict || (chain == "input" && strings.HasPrefix(name, "ban_forward")) || (chain == "forward" && strings.HasPrefix(name, "ban_input")) {
		return "", "", false
	}
	return name, verdict, true
}

func validManagedRuleCounts(rules map[string]int, sequences map[string][]string, markers map[string]int) bool {
	expectedInput := []string{"allow_v4:return", "protected_v4:return", "ban_input_v4:drop", "allow_v6:return", "protected_v6:return", "ban_input_v6:drop"}
	expectedForward := []string{"allow_v4:return", "protected_v4:return", "ban_forward_v4:drop", "allow_v6:return", "protected_v6:return", "ban_forward_v6:drop"}
	return rules["guard_policy"] == 1+markers[policyComment] && markers[infrastructureComment] == 1 && markers[policyComment] <= 1 &&
		rules["input"] == len(expectedInput) && rules["forward"] == len(expectedForward) && len(rules) == 3 &&
		reflect.DeepEqual(sequences["input"], expectedInput) && reflect.DeepEqual(sequences["forward"], expectedForward)
}

func isForeignPacketPath(kind string, head nftHeader) bool {
	return kind == "chain" && head.chainType == "filter" && (head.hook == "input" || head.hook == "forward")
}

func nftElement(body json.RawMessage, now time.Time) (netip.Prefix, *int64, bool) {
	var value struct {
		Elem    json.RawMessage `json:"elem"`
		Expires *int64          `json:"expires"`
	}
	if json.Unmarshal(body, &value) != nil {
		return netip.Prefix{}, nil, false
	}
	prefix, nestedExpires, ok := canonicalElementPrefix(value.Elem)
	if !ok {
		return netip.Prefix{}, nil, false
	}
	expires := value.Expires
	if expires == nil {
		expires = nestedExpires
	}
	if expires == nil {
		return prefix, nil, true
	}
	until := expiryUnixMicro(now, *expires)
	return prefix, &until, until > 0
}

func nftSetElements(body json.RawMessage, now time.Time) ([]element, bool) {
	var value struct {
		Elem json.RawMessage `json:"elem"`
	}
	if json.Unmarshal(body, &value) != nil || len(value.Elem) == 0 || string(value.Elem) == "null" {
		return nil, true
	}
	var rawElements []json.RawMessage
	if json.Unmarshal(value.Elem, &rawElements) != nil {
		return nil, false
	}
	result := make([]element, 0, len(rawElements))
	for _, rawElement := range rawElements {
		prefix, expires, ok := canonicalSetElement(rawElement)
		if !ok {
			return nil, false
		}
		if expires != nil {
			until := expiryUnixMicro(now, *expires)
			if until <= 0 {
				return nil, false
			}
			expires = &until
		}
		result = append(result, element{prefix: prefix, expires: expires})
	}
	return result, true
}

// The selected nft 1.0.6 JSON target emits remaining native-set expiry as
// whole seconds. Truncating the local read clock to that unit reconstructs a
// stable absolute expiry across successive reads of one unchanged element.
func expiryUnixMicro(now time.Time, remainingSeconds int64) int64 {
	return now.Truncate(time.Second).Add(time.Duration(remainingSeconds) * time.Second).UnixMicro()
}

func canonicalSetElement(raw json.RawMessage) (netip.Prefix, *int64, bool) {
	prefix, expires, ok := canonicalElementPrefix(raw)
	if ok {
		return prefix, expires, true
	}
	var wrapped struct {
		Elem    json.RawMessage `json:"elem"`
		Expires *int64          `json:"expires"`
	}
	if json.Unmarshal(raw, &wrapped) != nil || len(wrapped.Elem) == 0 {
		return netip.Prefix{}, nil, false
	}
	prefix, nestedExpires, ok := canonicalElementPrefix(wrapped.Elem)
	if !ok {
		return netip.Prefix{}, nil, false
	}
	if wrapped.Expires != nil {
		nestedExpires = wrapped.Expires
	}
	return prefix, nestedExpires, true
}

func canonicalElementPrefix(raw json.RawMessage) (netip.Prefix, *int64, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parseElementPrefix(text, 0)
	}
	var structured struct {
		Val    string `json:"val"`
		Addr   string `json:"addr"`
		Prefix *struct {
			Addr string `json:"addr"`
			Len  int    `json:"len"`
		} `json:"prefix"`
		Expires *int64 `json:"expires"`
	}
	if json.Unmarshal(raw, &structured) != nil {
		return netip.Prefix{}, nil, false
	}
	if structured.Prefix != nil {
		prefix, _, ok := parseElementPrefix(structured.Prefix.Addr, structured.Prefix.Len)
		return prefix, structured.Expires, ok
	}
	if structured.Val != "" {
		prefix, _, ok := parseElementPrefix(structured.Val, 0)
		return prefix, structured.Expires, ok
	}
	prefix, _, ok := parseElementPrefix(structured.Addr, 0)
	return prefix, structured.Expires, ok
}

func parseElementPrefix(text string, length int) (netip.Prefix, *int64, bool) {
	prefix, err := netip.ParsePrefix(text)
	if err != nil {
		address, addressErr := netip.ParseAddr(text)
		if addressErr != nil {
			return netip.Prefix{}, nil, false
		}
		prefix = netip.PrefixFrom(address, address.BitLen())
	}
	if length != 0 {
		if length < 0 || length > prefix.Addr().BitLen() {
			return netip.Prefix{}, nil, false
		}
		prefix = netip.PrefixFrom(prefix.Addr(), length)
	}
	if prefix != prefix.Masked() {
		return netip.Prefix{}, nil, false
	}
	return prefix, nil, true
}

func knownManager(head nftHeader) string {
	text := strings.ToLower(head.table + "/" + head.name + "/" + head.chain)
	if strings.Contains(text, "ufw") {
		return "ufw"
	}
	if strings.Contains(text, "docker") {
		return "docker"
	}
	return ""
}

func (s rulesetState) managedState() (firewall.ManagedState, error) {
	spec := firewall.ManagedStateSpec{}
	if s.managed {
		infrastructure, err := firewall.NewInfrastructureObservation(firewall.InfrastructureObservationSpec{Backend: firewall.BackendKindNftablesNative, OwnerVersion: firewall.ManagedOwnerVersionV1, SchemaVersion: firewall.ManagedInfrastructureSchemaVersionV1, Digest: sha256hex([]byte(infrastructureV))})
		if err != nil {
			return firewall.ManagedState{}, err
		}
		spec.Infrastructure = &infrastructure
		if s.policy {
			policy, err := firewall.NewPolicyObservation(firewall.PolicyObservationSpec{RelationDigest: policyDigest(s.sets)})
			if err != nil {
				return firewall.ManagedState{}, err
			}
			spec.Policy = &policy
		}
		for _, target := range targetObservations(s.sets) {
			spec.Targets = append(spec.Targets, target)
		}
	}
	return firewall.NewManagedState(spec)
}

func policyDigest(sets map[string][]element) string {
	var parts []string
	for _, name := range []string{"allow_v4", "allow_v6", "protected_v4", "protected_v6"} {
		for _, item := range sets[name] {
			parts = append(parts, name+":"+item.prefix.String())
		}
	}
	sort.Strings(parts)
	return sha256hex([]byte("guard-nftables-policy/v1\n" + strings.Join(parts, "\n")))
}

func targetObservations(sets map[string][]element) []firewall.TargetObservation {
	type observed struct {
		scopes  []firewall.ManagedScope
		expires *int64
	}
	all := make(map[netip.Prefix]observed)
	for name, values := range sets {
		var scope firewall.ManagedScope
		switch name {
		case "ban_input_v4", "ban_input_v6":
			scope = firewall.ManagedScopeInput
		case "ban_forward_v4", "ban_forward_v6":
			scope = firewall.ManagedScopeForward
		default:
			continue
		}
		for _, value := range values {
			prior := all[value.prefix]
			prior.scopes = append(prior.scopes, scope)
			if value.expires != nil {
				prior.expires = value.expires
			}
			all[value.prefix] = prior
		}
	}
	result := make([]firewall.TargetObservation, 0, len(all))
	for prefix, value := range all {
		mode := firewall.ManagedTimeoutNone
		if value.expires != nil {
			mode = firewall.ManagedTimeoutNative
		}
		observation, err := firewall.NewTargetObservation(firewall.TargetObservationSpec{Target: prefix, TimeoutMode: mode, EffectiveUntilUnixMicro: value.expires, Scopes: value.scopes})
		if err == nil {
			result = append(result, observation)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Target().String() < result[j].Target().String() })
	return result
}

func sha256hex(value []byte) string { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
