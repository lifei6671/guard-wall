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
exit $LASTEXITCODE
