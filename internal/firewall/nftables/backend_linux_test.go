//go:build linux

package nftables

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/enforcer"
	"github.com/lifei6671/guard-wall/internal/firewall"
)

const emptyRuleset = `{"nftables":[{"metainfo":{"version":"1.0.2"}}]}`

func TestGoldenStateScriptMatchesInfrastructureBatch(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate backend_linux_test.go")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	scriptPath := filepath.Join(root, "tests", "integration", "nftables", "golden-state.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read Golden State script: %v", err)
	}
	scriptText := strings.ReplaceAll(string(script), "\r\n", "\n")

	const start = "ip netns exec \"$router\" nft -f - <<'NFT'\n"
	const end = "\nNFT\n"
	if strings.Count(scriptText, start) != 1 {
		t.Fatalf("Golden State infrastructure heredoc start count = %d, want 1", strings.Count(scriptText, start))
	}
	_, batch, found := strings.Cut(scriptText, start)
	if !found {
		t.Fatal("Golden State infrastructure heredoc start is missing")
	}
	batch, _, found = strings.Cut(batch, end)
	if !found {
		t.Fatal("Golden State infrastructure heredoc end is missing")
	}
	if batch+"\n" != infrastructureBatch() {
		t.Fatal("Golden State infrastructure layout drifted from infrastructureBatch")
	}
}

func managedInfrastructureRuleset() string {
	entries := []string{`{"metainfo":{"version":"1.0.2"}}`, `{"table":{"family":"inet","name":"guard"}}`}
	for _, set := range managedSets {
		entries = append(entries, `{"set":{"family":"inet","table":"guard","name":"`+set+`"}}`)
	}
	for _, chain := range managedChains {
		entries = append(entries, `{"chain":{"family":"inet","table":"guard","name":"`+chain+`"}}`)
	}
	entries = append(entries, `{"rule":{"family":"inet","table":"guard","chain":"guard_policy","comment":"guard/v1 infrastructure/v1"}}`)
	return `{"nftables":[` + strings.Join(entries, ",") + `]}`
}

func TestProbeUsesFixedNftCommandAndRejectsKnownManagers(t *testing.T) {
	runner := &scriptedRunner{outputs: [][]byte{[]byte("nftables v1.0.2\n"), []byte(emptyRuleset)}}
	backend := newBackend(runner)
	capabilities, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	if capabilities.Backend() != firewall.BackendKindNftablesNative || !capabilities.MutationReady() {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if got := strings.Join(runner.calls[0].args, " "); got != "--version" {
		t.Fatalf("version command = %q", got)
	}
	if got := strings.Join(runner.calls[1].args, " "); got != "--json list ruleset" {
		t.Fatalf("ruleset command = %q", got)
	}

	manager := &scriptedRunner{outputs: [][]byte{[]byte("nftables v1.0.2\n"), []byte(`{"nftables":[{"table":{"family":"inet","name":"ufw-user-input"}}]}`)}}
	capabilities, err = newBackend(manager).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe manager: %v", err)
	}
	if capabilities.MutationReady() || capabilities.UFWIntegrationProven() {
		t.Fatal("known UFW manager did not fail mutation readiness closed")
	}

	foreignPath := &scriptedRunner{outputs: [][]byte{[]byte("nftables v1.0.2\n"), []byte(`{"nftables":[{"chain":{"family":"inet","table":"foreign","name":"input","type":"filter","hook":"input","prio":0,"policy":"accept"}}]}`)}}
	capabilities, err = newBackend(foreignPath).Probe(context.Background())
	if err != nil || capabilities.MutationReady() {
		t.Fatalf("unknown foreign packet path = (%#v, %v)", capabilities, err)
	}
}

func TestSnapshotRejectsForeignOrDriftedGuardTable(t *testing.T) {
	foreign := &scriptedRunner{outputs: [][]byte{[]byte(`{"nftables":[{"table":{"family":"inet","name":"guard","comment":"foreign/v1"}}]}`)}}
	if _, err := newBackend(foreign).Snapshot(context.Background()); !errors.Is(err, enforcer.ErrMutationBackendOwnershipConflict) {
		t.Fatalf("foreign table error = %v, want ownership conflict", err)
	}

	drifted := &scriptedRunner{outputs: [][]byte{[]byte(`{"nftables":[{"table":{"family":"inet","name":"guard","comment":"guard/v1"}}]}`)}}
	if _, err := newBackend(drifted).Snapshot(context.Background()); !errors.Is(err, enforcer.ErrMutationBackendOwnershipConflict) {
		t.Fatalf("drifted table error = %v, want ownership conflict", err)
	}
}

func TestParseRulesetCanonicalizesForeignDynamicFieldsAndOrder(t *testing.T) {
	first := []byte(`{"nftables":[
{"metainfo":{"version":"1.0.2"}},
{"table":{"family":"inet","name":"ambient","handle":11}},
{"chain":{"family":"inet","table":"ambient","name":"audit","handle":12}},
{"set":{"family":"inet","table":"ambient","name":"allow","type":"ipv4_addr","handle":14,"elem":["198.51.100.1","198.51.100.2"]}},
{"flowtable":{"family":"inet","table":"ambient","name":"ft","handle":15}},
{"quota":{"family":"inet","table":"ambient","name":"quota","handle":16}},
{"rule":{"family":"inet","table":"ambient","chain":"audit","handle":13,"expr":[{"counter":{"packets":1,"bytes":2}},{"accept":null}]}}]}`)
	second := []byte(`{"nftables":[
{"rule":{"chain":"audit","table":"ambient","family":"inet","handle":99,"expr":[{"counter":{"bytes":888,"packets":777}},{"accept":null}]}},
{"quota":{"name":"quota","table":"ambient","family":"inet","handle":94}},
{"flowtable":{"name":"ft","table":"ambient","family":"inet","handle":96}},
{"set":{"name":"allow","table":"ambient","family":"inet","handle":95,"type":"ipv4_addr","elem":["198.51.100.2","198.51.100.1"]}},
{"chain":{"name":"audit","table":"ambient","family":"inet","handle":98}},
{"table":{"name":"ambient","family":"inet","handle":97}},
{"metainfo":{"version":"1.0.2"}}]}`)
	left, err := parseRuleset(first, time.Now())
	if err != nil || left.ownershipConflict || left.hasUnknownPacketPath {
		t.Fatalf("first parse = (%#v, %v)", left, err)
	}
	right, err := parseRuleset(second, time.Now())
	if err != nil || right.ownershipConflict || right.hasUnknownPacketPath {
		t.Fatalf("second parse = (%#v, %v)", right, err)
	}
	if left.foreignDigest != right.foreignDigest {
		t.Fatalf("foreign digest changed for dynamic fields and output order: %s != %s", left.foreignDigest, right.foreignDigest)
	}
}

func TestParseRulesetForeignDigestPreservesRuleOrder(t *testing.T) {
	first := []byte(`{"nftables":[
{"table":{"family":"inet","name":"ambient"}},
{"chain":{"family":"inet","table":"ambient","name":"audit"}},
{"rule":{"family":"inet","table":"ambient","chain":"audit","expr":[{"counter":{"packets":1,"bytes":2}},{"accept":null}]}},
{"rule":{"family":"inet","table":"ambient","chain":"audit","expr":[{"counter":{"packets":3,"bytes":4}},{"drop":null}]}}]}`)
	second := []byte(`{"nftables":[
{"table":{"family":"inet","name":"ambient"}},
{"chain":{"family":"inet","table":"ambient","name":"audit"}},
{"rule":{"family":"inet","table":"ambient","chain":"audit","expr":[{"counter":{"packets":30,"bytes":40}},{"drop":null}]}},
{"rule":{"family":"inet","table":"ambient","chain":"audit","expr":[{"counter":{"packets":10,"bytes":20}},{"accept":null}]}}]}`)
	left, err := parseRuleset(first, time.Now())
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	right, err := parseRuleset(second, time.Now())
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if left.foreignDigest == right.foreignDigest {
		t.Fatal("foreign digest did not preserve rule order")
	}
}

func TestApplyInfrastructureUsesOneInternalBatch(t *testing.T) {
	parsed, err := parseRuleset([]byte(managedInfrastructureRuleset()), time.Now())
	if err != nil || parsed.ownershipConflict || !parsed.managed {
		t.Fatalf("managed fixture parse = (%#v, %v)", parsed, err)
	}
	runner := &scriptedRunner{outputs: [][]byte{
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset), // Probe
		[]byte(emptyRuleset),                              // authorization basis snapshot
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset), // Apply re-Probe
		[]byte(emptyRuleset),                   // immediate Apply re-observation
		[]byte{},                               // one internal batch
		[]byte(managedInfrastructureRuleset()), // confirmed post-state snapshot
	}}
	backend := newBackend(runner)
	capabilities, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe(): %v", err)
	}
	snapshot, err := backend.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot(): %v", err)
	}
	plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatalf("AuthorizeInfrastructureMutation(): %v", err)
	}
	result := backend.Apply(context.Background(), plan)
	if result.Status() != firewall.MutationStatusConfirmed || result.MutationDigest() != plan.Digest() {
		t.Fatalf("Apply() result = %#v calls=%d", result, len(runner.calls))
	}
	if len(runner.calls) != 8 {
		t.Fatalf("calls = %d, want 8", len(runner.calls))
	}
	batch := runner.calls[6]
	if got := strings.Join(batch.args, " "); got != "--file -" || !strings.Contains(string(batch.input), "add table inet guard") {
		t.Fatalf("batch = (%q, %q)", got, batch.input)
	}
	if strings.Contains(string(batch.input), "sh ") || strings.Contains(string(batch.input), "exec ") {
		t.Fatalf("batch contains a shell escape: %q", batch.input)
	}
}

func TestApplyDispatchFailureIsUnknown(t *testing.T) {
	runner := &scriptedRunner{outputs: [][]byte{
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset),
		[]byte(emptyRuleset),
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset),
		[]byte(emptyRuleset),
	}, errAt: map[int]error{6: errors.New("private nft failure")}}
	backend := newBackend(runner)
	capabilities, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	result := backend.Apply(context.Background(), plan)
	if result.Status() != firewall.MutationStatusUnknown {
		t.Fatalf("Apply failure result = %q, want unknown", result.Status())
	}
}

func TestApplyForeignDriftAfterDispatchIsUnknown(t *testing.T) {
	foreignAfter := strings.TrimSuffix(managedInfrastructureRuleset(), `]}`) + `,{"table":{"family":"inet","name":"foreign_after"}}]}`
	runner := &scriptedRunner{outputs: [][]byte{
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset),
		[]byte(emptyRuleset),
		[]byte("nftables v1.0.2\n"), []byte(emptyRuleset),
		[]byte(emptyRuleset), []byte{}, []byte(foreignAfter),
	}}
	backend := newBackend(runner)
	capabilities, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := firewall.AuthorizeInfrastructureMutation(capabilities, snapshot, snapshot.Digest(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if result := backend.Apply(context.Background(), plan); result.Status() != firewall.MutationStatusUnknown {
		t.Fatalf("Apply foreign-drift result = %q, want unknown", result.Status())
	}
}

func TestRemoveReobservesAndConfirmsManagedAbsence(t *testing.T) {
	managed := managedInfrastructureRuleset()
	runner := &scriptedRunner{outputs: [][]byte{
		[]byte("nftables v1.0.2\n"), []byte(managed), // authorization Probe
		[]byte(managed),                              // authorization Snapshot
		[]byte("nftables v1.0.2\n"), []byte(managed), // Remove re-Probe
		[]byte(managed),                // Remove basis Snapshot
		[]byte{}, []byte(emptyRuleset), // delete batch and confirmed post Snapshot
	}}
	backend := newBackend(runner)
	capabilities, err := backend.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := backend.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	authorization, alreadyAbsent, err := firewall.AuthorizeManagedRemoval(capabilities, snapshot, firewall.ManagedOwnerVersionV1)
	if err != nil || alreadyAbsent || authorization == nil {
		t.Fatalf("AuthorizeManagedRemoval() = (%v, %v, %v)", authorization, alreadyAbsent, err)
	}
	result := backend.RemoveManagedInfrastructure(context.Background(), authorization)
	if result.Status() != firewall.MutationStatusConfirmed || result.MutationDigest() != authorization.Digest() {
		t.Fatalf("Remove result = %#v", result)
	}
	if len(runner.calls) != 8 || strings.Join(runner.calls[6].args, " ") != "--file -" || string(runner.calls[6].input) != "delete table inet guard\n" {
		t.Fatalf("Remove calls = %#v", runner.calls)
	}
}

func TestParseRulesetAcceptsCanonicalStructuredNftElements(t *testing.T) {
	raw := strings.TrimSuffix(managedInfrastructureRuleset(), `]}`) + `,
{"rule":{"family":"inet","table":"guard","chain":"guard_policy","comment":"guard/v1 policy/v1"}}]}`
	raw = strings.Replace(raw, `"name":"allow_v4"}}`, `"name":"allow_v4","elem":[{"prefix":{"addr":"198.51.100.0","len":24}}]}}`, 1)
	raw = strings.Replace(raw, `"name":"protected_v6"}}`, `"name":"protected_v6","elem":["::1"]}}`, 1)
	raw = strings.Replace(raw, `"name":"ban_input_v4"}}`, `"name":"ban_input_v4","elem":[{"elem":{"val":"203.0.113.7"},"expires":60}]}}`, 1)
	parsed, err := parseRuleset([]byte(raw), time.Now())
	if err != nil || parsed.ownershipConflict || !parsed.policy {
		t.Fatalf("parse structured elements = (%#v, %v)", parsed, err)
	}
	state, err := parsed.managedState()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := state.Policy(); !present {
		t.Fatal("policy marker was not observed")
	}
	targets := state.Targets()
	if len(targets) != 1 || targets[0].Target() != netip.MustParsePrefix("203.0.113.7/32") {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestTargetBatchUsesOnlyTypedCanonicalPrefix(t *testing.T) {
	prefix := netip.MustParsePrefix("2001:db8::/64")
	sets := targetSets(prefix, []firewall.ManagedScope{firewall.ManagedScopeInput, firewall.ManagedScopeForward})
	if strings.Join(sets, ",") != "ban_input_v6,ban_forward_v6" {
		t.Fatalf("target sets = %v", sets)
	}
}

func TestStrictLayoutValidatorsRejectDefinitionAndRuleDrift(t *testing.T) {
	if validManagedChain(nftHeader{name: "input", chainType: "filter", hook: "input", priority: 1, policy: "accept"}) {
		t.Fatal("priority drift was accepted")
	}
	if validManagedSet(nftHeader{name: "ban_input_v4"}, []byte(`{"type":"ipv4_addr","flags":["interval"]}`)) {
		t.Fatal("timeout-set flag drift was accepted")
	}
	valid := []byte(`{"expr":[{"match":{"op":"==","left":{"payload":{"protocol":"ip","field":"saddr"}},"right":"@ban_input_v4"}},{"drop":null}]}`)
	if !validManagedRule(nftHeader{chain: "input"}, valid) {
		t.Fatal("canonical fixed rule was rejected")
	}
	drifted := []byte(`{"expr":[{"match":{"op":"==","left":{"payload":{"protocol":"ip","field":"daddr"}},"right":"@ban_input_v4"}},{"drop":null}]}`)
	if validManagedRule(nftHeader{chain: "input"}, drifted) {
		t.Fatal("rule expression drift was accepted")
	}
	sequences := map[string][]string{
		"input":   {"ban_input_v4:drop", "protected_v4:return", "allow_v4:return", "allow_v6:return", "protected_v6:return", "ban_input_v6:drop"},
		"forward": {"allow_v4:return", "protected_v4:return", "ban_forward_v4:drop", "allow_v6:return", "protected_v6:return", "ban_forward_v6:drop"},
	}
	if validManagedRuleCounts(map[string]int{"guard_policy": 1, "input": 6, "forward": 6}, sequences, map[string]int{infrastructureComment: 1}) {
		t.Fatal("ban-before-allow sequence drift was accepted")
	}
}

func TestExpiryUnixMicroStabilizesNftSecondGranularity(t *testing.T) {
	first := expiryUnixMicro(time.Unix(100, 100_000_000), 59)
	second := expiryUnixMicro(time.Unix(101, 900_000_000), 58)
	if first != second {
		t.Fatalf("expiry reconstruction drifted: %d != %d", first, second)
	}
}

type commandCall struct {
	input []byte
	args  []string
}

type scriptedRunner struct {
	outputs [][]byte
	calls   []commandCall
	errAt   map[int]error
}

func (r *scriptedRunner) run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandCall{input: append([]byte(nil), input...), args: append([]string(nil), args...)})
	if err := r.errAt[len(r.calls)-1]; err != nil {
		return nil, err
	}
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected nft command")
	}
	output := r.outputs[0]
	r.outputs = r.outputs[1:]
	return output, nil
}
