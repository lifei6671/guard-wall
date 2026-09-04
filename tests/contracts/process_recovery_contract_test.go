package contracts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var requiredFinalAssertions = []string{
	"desired_ban_projection",
	"normalized_enforcement_intent",
	"firewall_snapshot",
	"observed_infrastructure_state",
	"observed_policy_state",
	"observed_target_state",
	"decision_history",
	"retry_domain_state",
	"critical_audit",
}

var m0RecoveryCases = []recoveryCaseExpectation{
	{
		id: "M0-RECOVERY-001", scope: "sqlite_store",
		injectionPoints: []string{"committed_transaction_boundary", "uncommitted_transaction_boundary"},
		runner:          recoveryRunnerExpectation{packagePath: "./internal/store", buildTags: "integration", testName: "TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability", isolation: "docker_go_runner", command: "go test -tags=integration -count=1 -run '^TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability$' ./internal/store", sourcePath: "internal/store/sqlite_linux_integration_test.go"},
		expected: map[string]string{
			"committed_state":                  "readable_after_reopen",
			"uncommitted_state":                "absent_after_reopen",
			"duplicate_persistent_side_effect": "forbidden",
		},
	},
	{
		id: "M0-RECOVERY-002", scope: "source_receipt_checkpoint",
		injectionPoints: []string{"after_rotation_before_receipt", "after_receipt_before_checkpoint"},
		runner:          recoveryRunnerExpectation{packagePath: "./internal/source", buildTags: "integration", testName: "TestM0Recovery002SQLiteSourceGenerationTransitionSIGKILLRecovery", isolation: "docker_go_runner", command: "go test -tags=integration -count=1 -run '^TestM0Recovery002SQLiteSourceGenerationTransitionSIGKILLRecovery$' ./internal/source", sourcePath: "internal/source/sqlite_state_crash_linux_integration_test.go"},
		expected: map[string]string{
			"checkpoint":                       "does_not_advance_past_uncommitted_record",
			"delivery_identity":                "stable_after_reopen",
			"duplicate_persistent_side_effect": "forbidden",
		},
	},
	{
		id: "M0-RECOVERY-003", scope: "processing_unit_of_work",
		injectionPoints: []string{"after_processing_writes_before_commit"},
		runner:          recoveryRunnerExpectation{packagePath: "./internal/store", buildTags: "integration", testName: "TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay", isolation: "docker_go_runner", command: "go test -tags=integration -count=1 -run '^TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay$' ./internal/store", sourcePath: "internal/store/processing_crash_linux_integration_test.go"},
		expected: map[string]string{
			"uncommitted_processing_rows": "absent_after_reopen",
			"replay":                      "creates_one_committed_result",
			"checkpoint":                  "advances_only_after_commit",
		},
	},
	{
		id: "M0-RECOVERY-004", scope: "clean_target_linux",
		injectionPoints: []string{"guard_agent_sigkill", "guard_enforcer_sigkill"},
		runner:          recoveryRunnerExpectation{packagePath: "./internal/enforcer", buildTags: "integration,nftables", testName: "TestEnforcerRuntimeNftablesIntegration/M0-RECOVERY-004", isolation: "disposable_docker_nftables", command: "tests/integration/nftables/run.sh", sourcePath: "internal/enforcer/runtime_nftables_integration_test.go", runnerPath: "tests/integration/nftables/run.sh"},
		expected: map[string]string{
			"guard_owned_socket":           "readable_after_reopen",
			"sqlite_state":                 "readable_after_reopen",
			"guard_owned_nftables_objects": "identity_preserved",
			"cleanup":                      "identity_guarded",
		},
	},
}

type recoveryContract struct {
	Version                 string      `yaml:"version"`
	Status                  string      `yaml:"status"`
	M0GateInput             *bool       `yaml:"m0_gate_input"`
	SuiteRunner             suiteRunner `yaml:"suite_runner"`
	Contract                contract    `yaml:"contract"`
	RequiredFinalAssertions []string    `yaml:"required_final_assertions"`
	Cases                   []struct {
		ID              string            `yaml:"id"`
		Scope           string            `yaml:"scope"`
		Signal          string            `yaml:"signal"`
		Restart         string            `yaml:"restart"`
		InjectionPoints []string          `yaml:"injection_points"`
		Expected        map[string]string `yaml:"expected"`
		FinalState      map[string]string `yaml:"final_state"`
		Runner          recoveryRunner    `yaml:"runner"`
		ReopenCount     int               `yaml:"reopen_count"`
		ReadbackScope   []string          `yaml:"readback_scope"`
	} `yaml:"cases"`
}

type suiteRunner struct {
	Command   string `yaml:"command"`
	Isolation string `yaml:"isolation"`
}

type recoveryRunner struct {
	PackagePath string `yaml:"package"`
	BuildTags   string `yaml:"build_tags"`
	TestName    string `yaml:"test_name"`
	Isolation   string `yaml:"isolation"`
	Command     string `yaml:"command"`
	SourcePath  string `yaml:"source_path"`
	RunnerPath  string `yaml:"runner_path"`
}

type contract struct {
	Path     string   `yaml:"path"`
	Sections []string `yaml:"sections"`
}

type recoveryCaseExpectation struct {
	id              string
	scope           string
	injectionPoints []string
	runner          recoveryRunnerExpectation
	expected        map[string]string
}

type recoveryRunnerExpectation struct {
	packagePath string
	buildTags   string
	testName    string
	isolation   string
	command     string
	sourcePath  string
	runnerPath  string
}

func TestM0ProcessRecoveryManifest(t *testing.T) {
	manifest := loadRecoveryContract(t, "m0-process-recovery.yaml")
	if manifest.Version != "guard.m0-process-recovery.v1" || manifest.Status != "specified" {
		t.Fatalf("M0 manifest identity = version %q, status %q", manifest.Version, manifest.Status)
	}
	if manifest.M0GateInput != nil {
		t.Fatal("M0 manifest must not declare itself outside the M0 gate")
	}
	if manifest.SuiteRunner != (suiteRunner{
		Command:   `.\\scripts\\test-m0-process-recovery.ps1`,
		Isolation: "docker_go_runner_and_disposable_docker_nftables",
	}) {
		t.Fatalf("M0 suite runner = %#v", manifest.SuiteRunner)
	}
	assertM0SuiteRunner(t, manifest.SuiteRunner, m0RecoveryCases)
	assertContract(t, manifest.Contract, []string{"12.3", "17.3"}, "M0")
	assertRecoveryCases(t, manifest, m0RecoveryCases)
}

func TestExtendedCrashMatrixStaysOutsideM0Gate(t *testing.T) {
	manifest := loadRecoveryContract(t, "phase1-extended-crash-matrix.yaml")
	if manifest.Version != "guard.phase1-extended-crash-matrix.v1" || manifest.Status != "specified_for_m7_m10" {
		t.Fatalf("extended matrix identity = version %q, status %q", manifest.Version, manifest.Status)
	}
	if manifest.M0GateInput == nil || *manifest.M0GateInput {
		t.Fatal("extended crash matrix must remain outside the M0 gate")
	}
	assertContract(t, manifest.Contract, []string{"12.4"}, "extended matrix")
	if len(manifest.Cases) != 8 {
		t.Fatalf("extended matrix case count = %d, want 8", len(manifest.Cases))
	}
	for index, recoveryCase := range manifest.Cases {
		wantID := []string{
			"P1-CRASH-001",
			"P1-CRASH-002",
			"P1-CRASH-003",
			"P1-CRASH-004",
			"P1-CRASH-005",
			"P1-CRASH-006",
			"P1-CRASH-007",
			"P1-CRASH-008",
		}[index]
		if recoveryCase.ID != wantID {
			t.Fatalf("extended matrix case %d ID = %q, want %q", index, recoveryCase.ID, wantID)
		}
	}
	assertRequiredFinalAssertions(t, manifest)
}

func loadRecoveryContract(t *testing.T, filename string) recoveryContract {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract test source path")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), filename))
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	var manifest recoveryContract
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	return manifest
}

func assertContract(t *testing.T, got contract, wantSections []string, label string) {
	t.Helper()
	if got.Path != "docs/contracts/guard-phase-1-m0-contract-freeze-v0.3.md" {
		t.Fatalf("%s contract path = %q", label, got.Path)
	}
	assertStrings(t, got.Sections, wantSections, label+" contract sections")
}

func assertRecoveryCases(t *testing.T, manifest recoveryContract, wants []recoveryCaseExpectation) {
	t.Helper()
	assertStrings(t, manifest.RequiredFinalAssertions, requiredFinalAssertions, "required final assertions")
	if len(manifest.Cases) != len(wants) {
		t.Fatalf("M0 recovery case count = %d, want %d", len(manifest.Cases), len(wants))
	}
	for index, recoveryCase := range manifest.Cases {
		want := wants[index]
		if recoveryCase.ID != want.id || recoveryCase.Scope != want.scope {
			t.Fatalf("M0 recovery case %d = ID %q scope %q, want ID %q scope %q", index, recoveryCase.ID, recoveryCase.Scope, want.id, want.scope)
		}
		if recoveryCase.Signal != "SIGKILL" || recoveryCase.Restart != "process_reopen_required" {
			t.Fatalf("%s lifecycle = signal %q restart %q", recoveryCase.ID, recoveryCase.Signal, recoveryCase.Restart)
		}
		assertStrings(t, recoveryCase.InjectionPoints, want.injectionPoints, recoveryCase.ID+" injection points")
		assertRunner(t, recoveryCase.ID, recoveryCase.Runner, want.runner)
		if recoveryCase.ReopenCount <= 0 || len(recoveryCase.ReadbackScope) == 0 {
			t.Fatalf("%s runner recovery metadata is incomplete", recoveryCase.ID)
		}
		assertStringMap(t, recoveryCase.Expected, want.expected, recoveryCase.ID+" expected state")
		assertFinalState(t, recoveryCase.ID, recoveryCase.FinalState)
	}
}

func assertRunner(t *testing.T, caseID string, got recoveryRunner, want recoveryRunnerExpectation) {
	t.Helper()
	if got.PackagePath != want.packagePath || got.BuildTags != want.buildTags || got.TestName != want.testName || got.Isolation != want.isolation || got.Command != want.command || got.SourcePath != want.sourcePath || got.RunnerPath != want.runnerPath {
		t.Fatalf("%s runner = %#v, want %#v", caseID, got, want)
	}
	assertRunnerSource(t, caseID, got)
}

func assertRunnerSource(t *testing.T, caseID string, runner recoveryRunner) {
	t.Helper()
	root := contractProjectRoot(t)
	sourcePath := filepath.Join(root, filepath.FromSlash(runner.SourcePath))
	parsed, err := parser.ParseFile(token.NewFileSet(), sourcePath, nil, 0)
	if err != nil {
		t.Fatalf("%s parse runner source: %v", caseID, err)
	}
	outerTest := strings.Split(runner.TestName, "/")[0]
	found := false
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == outerTest {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s runner source lacks %s", caseID, outerTest)
	}
	if !strings.Contains(runner.TestName, "/") {
		return
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("%s read runner source: %v", caseID, err)
	}
	subtest := strings.TrimPrefix(runner.TestName, outerTest+"/")
	if !strings.Contains(string(content), `t.Run("`+subtest+`"`) {
		t.Fatalf("%s runner source lacks subtest %q", caseID, subtest)
	}
	runnerContent, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(runner.RunnerPath)))
	if err != nil {
		t.Fatalf("%s read integration runner: %v", caseID, err)
	}
	if !strings.Contains(string(runnerContent), "--- PASS: "+runner.TestName) {
		t.Fatalf("%s integration runner does not require %s", caseID, runner.TestName)
	}
}

func contractProjectRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve contract project root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}

func assertM0SuiteRunner(t *testing.T, runner suiteRunner, cases []recoveryCaseExpectation) {
	t.Helper()
	path := filepath.Join(contractProjectRoot(t), filepath.FromSlash("scripts/test-m0-process-recovery.ps1"))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read M0 suite runner: %v", err)
	}
	script := string(content)
	for _, recoveryCase := range cases[:3] {
		for _, token := range []string{recoveryCase.runner.testName, recoveryCase.runner.packagePath} {
			if !strings.Contains(script, token) {
				t.Fatalf("M0 suite runner lacks %s token %q", recoveryCase.id, token)
			}
		}
	}
	for _, token := range []string{
		"go test -count=1 -v ./tests/contracts",
		"Assert-RequiredTestPassed",
		"TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability",
		"TestM0Recovery002SQLiteSourceGenerationTransitionSIGKILLRecovery",
		"TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay",
		"tests/integration/nftables/Dockerfile",
		"docker run --rm",
		"--network none",
		"--cap-drop ALL",
		"--cap-add NET_ADMIN",
		"--cap-add NET_RAW",
		"--cap-add SYS_ADMIN",
		"--cap-add SETUID",
		"--cap-add SETGID",
		"--cap-add CHOWN",
		"--read-only",
		"--tmpfs /run:rw,nosuid,nodev,noexec,size=16m",
		"--tmpfs /tmp:rw,exec,nosuid,nodev,size=1g",
		"--security-opt no-new-privileges:true",
		"--security-opt seccomp=unconfined",
	} {
		if !strings.Contains(script, token) {
			t.Fatalf("M0 suite runner lacks token %q", token)
		}
	}
	dockerfile, err := os.ReadFile(filepath.Join(contractProjectRoot(t), filepath.FromSlash("tests/integration/nftables/Dockerfile")))
	if err != nil {
		t.Fatalf("read M0-RECOVERY-004 Dockerfile: %v", err)
	}
	for _, token := range []string{
		"COPY tests/integration/nftables/run.sh /usr/local/bin/run-nftables-integration",
		`ENTRYPOINT ["/usr/local/bin/run-nftables-integration"]`,
	} {
		if !strings.Contains(string(dockerfile), token) {
			t.Fatalf("M0-RECOVERY-004 Dockerfile lacks token %q", token)
		}
	}
}

func assertRequiredFinalAssertions(t *testing.T, manifest recoveryContract) {
	t.Helper()
	assertStrings(t, manifest.RequiredFinalAssertions, requiredFinalAssertions, "required final assertions")
	for _, recoveryCase := range manifest.Cases {
		assertFinalState(t, recoveryCase.ID, recoveryCase.FinalState)
	}
}

func assertFinalState(t *testing.T, caseID string, finalState map[string]string) {
	t.Helper()
	if len(finalState) != len(requiredFinalAssertions) {
		t.Fatalf("%s final state field count = %d, want %d", caseID, len(finalState), len(requiredFinalAssertions))
	}
	for _, assertion := range requiredFinalAssertions {
		if finalState[assertion] == "" {
			t.Fatalf("%s final state %q is missing", caseID, assertion)
		}
	}
}

func assertStrings(t *testing.T, got, want []string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count = %d, want %d", label, len(got), len(want))
	}
	for index, value := range want {
		if got[index] != value {
			t.Fatalf("%s[%d] = %q, want %q", label, index, got[index], value)
		}
	}
}

func assertStringMap(t *testing.T, got, want map[string]string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s field count = %d, want %d", label, len(got), len(want))
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Fatalf("%s[%q] = %q, want %q", label, key, got[key], wantValue)
		}
	}
}
