# M0-B Spike Environment Readiness

> Preliminary worktree evidence. This file records the capability-discovery snapshot taken
> before the B3/B4 Spikes;
> it does not claim that B1–B4 are Verified.

## Host

- Base commit: `3aca38a`
- Host shell: PowerShell on Windows 11
- Go: `go1.27.0 windows/amd64`
- Host `sqlite3` CLI: not installed
- Host Docker CLI: not installed
- Host `nft` / `systemctl`: not available, as expected on Windows

## WSL2

Available distributions:

- Ubuntu 22.04, running, WSL2
- Debian, running, WSL2

Ubuntu probe:

- Kernel: `6.18.33.2-microsoft-standard-WSL2`
- root access: available through the WSL root user
- nftables: `v1.0.2`
- iproute2: `5.15.0`
- systemd: `249`, PID 1
- SQLite CLI: not installed
- Go: not installed

Debian capability discovery also found nftables and systemd, but no SQLite CLI or Go.

## Consequence

- B1 can run SQLite semantic Spikes on the Windows Python standard-library driver, but the
  future Go driver and Linux crash/power-loss domains remain unverified.
- B2 can run a Go standard-library identity Spike on Windows. Cross-runtime vectors remain
  possible because the algorithm is deterministic.
- B3 can use a temporary WSL2 network namespace. After this readiness capture, the isolated
  namespace Spike was executed and cleaned up; see [nftables-result.json](nftables-result.json).
- B4 can test Unix Socket peer credentials and systemd behavior in WSL2, but the exact
  production service hardening still requires a dedicated Linux release environment.

## Authorization boundary

Installing a SQLite CLI/Go toolchain in WSL, adding a Go SQLite dependency, or mutating even
an isolated nftables namespace must be recorded as an explicit Spike action. Host or production
Firewall mutation remains forbidden.
