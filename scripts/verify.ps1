$ErrorActionPreference = "Stop"

& docker compose -f compose.codex.yml up -d go-runner
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& docker compose -f compose.codex.yml exec -T go-runner go test ./...
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& docker compose -f compose.codex.yml exec -T go-runner go vet ./...
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& docker compose -f compose.codex.yml exec -T go-runner go build ./...
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& docker compose -f compose.codex.yml exec -T go-runner sh -ec 'CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/guard-wall-core ./cmd/guard-enforcer && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/guard-wall-agent ./cmd/guard-agent && test -x /tmp/guard-wall-core && test -x /tmp/guard-wall-agent'
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

& "$PSScriptRoot/verify-packaging.ps1"
exit $LASTEXITCODE
