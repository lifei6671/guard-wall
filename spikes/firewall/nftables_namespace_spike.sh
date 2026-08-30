#!/bin/sh
set -eu

namespace=${1:-guard-m0-b3}

if [ "$(id -u)" -ne 0 ]; then
  echo "must run as root" >&2
  exit 1
fi

command -v ip >/dev/null
command -v nft >/dev/null

cleanup() {
  ip netns delete "$namespace" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM

if ip netns list | grep -q "^${namespace}[[:space:]]"; then
  echo "namespace already exists: $namespace" >&2
  exit 1
fi

ip netns add "$namespace"
ip -n "$namespace" link set lo up

# A foreign table must survive all Guard-owned operations.
ip netns exec "$namespace" nft -f - <<'EOF'
add table inet foreign_owner
add chain inet foreign_owner input
add rule inet foreign_owner input counter
EOF

# The priority value is only a syntax/atomicity probe. Production priority is
# frozen only after the complete B3 behavior review.
ip netns exec "$namespace" nft -f - <<'EOF'
add table inet guard_m0
add set inet guard_m0 allow_v4 { type ipv4_addr; flags interval; }
add set inet guard_m0 ban_v4 { type ipv4_addr; flags interval,timeout; }
add chain inet guard_m0 input { type filter hook input priority 0; policy accept; }
add rule inet guard_m0 input ip saddr @allow_v4 return
add rule inet guard_m0 input ip saddr @ban_v4 drop
add element inet guard_m0 allow_v4 { 192.0.2.10 }
add element inet guard_m0 ban_v4 { 198.51.100.7 timeout 5m }
EOF

snapshot_before=$(ip netns exec "$namespace" nft list table inet guard_m0)
printf '%s\n' "$snapshot_before" | grep -q '192.0.2.10'
printf '%s\n' "$snapshot_before" | grep -q '198.51.100.7'

# Read back the canonical chain snapshot. This proves nft retained the allow
# rule before the ban rule; it does not replace the still-pending packet-path
# test in a production-equivalent hook/priority environment.
chain_snapshot=$(ip netns exec "$namespace" nft list chain inet guard_m0 input)
allow_line=$(printf '%s\n' "$chain_snapshot" | awk '/@allow_v4/ { print NR; exit }')
ban_line=$(printf '%s\n' "$chain_snapshot" | awk '/@ban_v4/ { print NR; exit }')
if [ -z "$allow_line" ] || [ -z "$ban_line" ] || [ "$allow_line" -ge "$ban_line" ]; then
  echo "allow rule was not retained before ban rule" >&2
  exit 1
fi

# A failing nft batch must not retain the valid first operation.
if ip netns exec "$namespace" nft -f - >/dev/null 2>&1 <<'EOF'
add element inet guard_m0 ban_v4 { 203.0.113.99 timeout 5m }
add rule inet guard_m0 missing_chain counter
EOF
then
  echo "invalid batch unexpectedly succeeded" >&2
  exit 1
fi

snapshot_after_failed_batch=$(ip netns exec "$namespace" nft list table inet guard_m0)
if printf '%s\n' "$snapshot_after_failed_batch" | grep -q '203.0.113.99'; then
  echo "failed batch left a partial element" >&2
  exit 1
fi

# Removing Guard-owned infrastructure must not affect the foreign table.
ip netns exec "$namespace" nft delete table inet guard_m0
ip netns exec "$namespace" nft list table inet foreign_owner >/dev/null
if ip netns exec "$namespace" nft list table inet guard_m0 >/dev/null 2>&1; then
  echo "Guard-owned table still exists" >&2
  exit 1
fi

cat <<EOF
status=PASS
namespace=$namespace
nft_version=$(nft --version)
checks=guard_table_apply,allow_before_ban_snapshot_order,timeout_syntax,atomic_failed_batch,foreign_preservation,managed_cleanup
not_verified=production_hook_priority,packet_path,crash_recovery,ownership_conflict,apply_confirm
EOF
