$ErrorActionPreference = "Stop"

$imageTag = "guard-wall-m0-process-recovery:local"
$completed = $false

function Assert-LastExitCode([string]$step) {
    if ($LASTEXITCODE -ne 0) {
        throw "$step failed with exit code $LASTEXITCODE"
    }
}

function Assert-RequiredTestPassed([string]$output, [string]$testName) {
    if ($output -match [regex]::Escape("--- SKIP: $testName")) {
        throw "$testName was skipped"
    }
    if ($output -notmatch [regex]::Escape("--- PASS: $testName")) {
        throw "$testName did not report PASS"
    }
}

& docker compose -f compose.codex.yml up -d go-runner
Assert-LastExitCode "start Docker Go runner"

$storeOutput = & docker compose -f compose.codex.yml exec -T go-runner go test `
    -tags=integration -count=1 -v `
    -run '^(TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability|TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay)$' `
    ./internal/store
$storeExitCode = $LASTEXITCODE
$storeOutput | Write-Output
if ($storeExitCode -ne 0) {
    throw "M0-RECOVERY-001 and M0-RECOVERY-003 failed with exit code $storeExitCode"
}
Assert-RequiredTestPassed ($storeOutput -join "`n") "TestM0Recovery001SQLiteLinuxSIGKILLReopenDurability"
Assert-RequiredTestPassed ($storeOutput -join "`n") "TestM0Recovery003SQLiteProcessingTransactionSIGKILLReplay"

$sourceOutput = & docker compose -f compose.codex.yml exec -T go-runner go test `
    -tags=integration -count=1 -v `
    -run '^TestM0Recovery002SQLiteSourceGenerationTransitionSIGKILLRecovery$' `
    ./internal/source
$sourceExitCode = $LASTEXITCODE
$sourceOutput | Write-Output
if ($sourceExitCode -ne 0) {
    throw "M0-RECOVERY-002 failed with exit code $sourceExitCode"
}
Assert-RequiredTestPassed ($sourceOutput -join "`n") "TestM0Recovery002SQLiteSourceGenerationTransitionSIGKILLRecovery"

& docker compose -f compose.codex.yml exec -T go-runner go test -count=1 -v ./tests/contracts
Assert-LastExitCode "M0 recovery contract guard"

try {
    & docker build --file tests/integration/nftables/Dockerfile --tag $imageTag .
    Assert-LastExitCode "build M0-RECOVERY-004 image"

    & docker run --rm `
        --network none `
        --cap-drop ALL `
        --cap-add NET_ADMIN `
        --cap-add NET_RAW `
        --cap-add SYS_ADMIN `
        --cap-add SETUID `
        --cap-add SETGID `
        --cap-add CHOWN `
        --read-only `
        --tmpfs /run:rw,nosuid,nodev,noexec,size=16m `
        --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g `
        --security-opt no-new-privileges:true `
        --security-opt seccomp=unconfined `
        $imageTag
    Assert-LastExitCode "M0-RECOVERY-004"

    $completed = $true
}
finally {
    & docker image rm $imageTag 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) {
        if ($completed) {
            throw "remove M0 process recovery image failed with exit code $LASTEXITCODE"
        }
        Write-Warning "remove M0 process recovery image failed with exit code $LASTEXITCODE"
    }
}
