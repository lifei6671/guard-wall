$ErrorActionPreference = "Stop"

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$systemdDirectory = Join-Path $repositoryRoot "packaging/systemd"
$coreUnit = Join-Path $systemdDirectory "guard-wall-core.service"
$agentUnit = Join-Path $systemdDirectory "guard-wall-agent.service"
$sysusers = Join-Path $systemdDirectory "guard-wall.sysusers.conf"
$config = Join-Path $repositoryRoot "packaging/config/guard-wall.yaml"

foreach ($path in @($coreUnit, $agentUnit, $sysusers, $config)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "missing packaging artifact: $path"
    }
}

foreach ($legacyUnit in @("guard-enforcer.service", "guard-agent.service")) {
    if (Test-Path -LiteralPath (Join-Path $systemdDirectory $legacyUnit) -PathType Leaf) {
        throw "legacy systemd unit must not remain: $legacyUnit"
    }
}

function Require-ExactLine {
    param(
        [string]$Path,
        [string]$Line
    )

    $content = Get-Content -LiteralPath $Path -Raw
    $pattern = '(?m)^' + [regex]::Escape($Line) + '\r?$'
    if (-not [regex]::IsMatch($content, $pattern)) {
        throw "missing required line in ${Path}: $Line"
    }
}

Require-ExactLine $coreUnit "Before=guard-wall-agent.service"
Require-ExactLine $coreUnit "ExecStart=/usr/local/bin/guard-wall-core"
Require-ExactLine $coreUnit "User=root"
Require-ExactLine $coreUnit "Group=guard"
Require-ExactLine $coreUnit "RuntimeDirectory=guard"
Require-ExactLine $coreUnit "RuntimeDirectoryMode=0750"
Require-ExactLine $coreUnit "CapabilityBoundingSet=CAP_NET_ADMIN"
Require-ExactLine $coreUnit "AmbientCapabilities=CAP_NET_ADMIN"
Require-ExactLine $coreUnit "ReadWritePaths=/run/guard"
Require-ExactLine $agentUnit "Requires=guard-wall-core.service"
Require-ExactLine $agentUnit "After=guard-wall-core.service"
Require-ExactLine $agentUnit "ExecStart=/usr/local/bin/guard-wall-agent --config /etc/guard/guard-wall.yaml"
Require-ExactLine $agentUnit "StateDirectory=guard"
Require-ExactLine $agentUnit "StateDirectoryMode=0750"
Require-ExactLine $agentUnit "ReadWritePaths=/var/lib/guard"
Require-ExactLine $agentUnit "CapabilityBoundingSet="
Require-ExactLine $agentUnit "AmbientCapabilities="
Require-ExactLine $sysusers "g guard -"
Require-ExactLine $sysusers 'u guard - "GuardWall service account" /nonexistent /usr/sbin/nologin'
Require-ExactLine $config "  database_path: /var/lib/guard/guard-wall.db"

if (Select-String -LiteralPath $agentUnit -SimpleMatch -Quiet -Pattern "ReadWritePaths=/etc/guard") {
    throw "agent unit must not grant write access to /etc/guard"
}
