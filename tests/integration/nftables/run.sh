#!/bin/sh
set -eu

test -f /.dockerenv
test ! -S /var/run/docker.sock

if [ "$(id -u)" -ne 0 ]; then
  echo "nftables integration runner must start as container root" >&2
  exit 1
fi

current_caps=$(capsh --print | sed -n 's/^Current: //p')
current_caps=${current_caps%%=*}
for capability in cap_net_admin cap_net_raw cap_sys_admin; do
  case ",$current_caps," in
    *",$capability,"*) ;;
    *)
      echo "nftables integration runner requires $capability" >&2
      exit 1
      ;;
  esac
done

# The workflow creates this container with --network none. nftables state is
# therefore confined to its disposable network namespace, never the runner.
nft list tables >/dev/null

export GUARD_NFTABLES_INTEGRATION=1
export GUARD_NFTABLES_ISOLATED=1

/usr/local/bin/run-nftables-golden-state

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
