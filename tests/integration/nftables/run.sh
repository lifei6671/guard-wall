#!/bin/sh
set -eu

test -f /.dockerenv
test ! -S /var/run/docker.sock

if [ "$(id -u)" -ne 0 ]; then
  echo "nftables integration runner must start as container root" >&2
  exit 1
fi

if ! capsh --print | grep -q 'cap_net_admin'; then
  echo "nftables integration runner requires CAP_NET_ADMIN" >&2
  exit 1
fi

# The workflow creates this container with --network none. nftables state is
# therefore confined to its disposable network namespace, never the runner.
nft list tables >/dev/null

export GUARD_NFTABLES_INTEGRATION=1
export GUARD_NFTABLES_ISOLATED=1

if ! go test -tags=integration,nftables -list '^TestNftablesBackendIntegration$' \
  ./internal/firewall/nftables | grep -qx 'TestNftablesBackendIntegration'; then
  echo "missing required TestNftablesBackendIntegration integration test" >&2
  exit 1
fi

if ! go test -tags=integration,nftables -list '^TestEnforcerRuntimeNftablesIntegration$' \
  ./internal/enforcer | grep -qx 'TestEnforcerRuntimeNftablesIntegration'; then
  echo "missing required TestEnforcerRuntimeNftablesIntegration integration test" >&2
  exit 1
fi

go test \
  -tags=integration,nftables \
  -count=1 \
  -v \
  ./internal/firewall/nftables

exec go test \
  -tags=integration,nftables \
  -count=1 \
  -v \
  -run '^TestEnforcerRuntimeNftablesIntegration$' \
  ./internal/enforcer
