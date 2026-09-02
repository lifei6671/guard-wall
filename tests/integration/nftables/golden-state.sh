#!/bin/sh
set -eu

# 该脚本仅运行在 --network none 的 disposable 容器中。所有 nftables 表、veth 和
# network namespace 均在容器内部创建，并由 cleanup 删除；不挂载 Docker socket 或宿主路径。
test -f /.dockerenv
test ! -S /var/run/docker.sock

if [ "$(id -u)" -ne 0 ]; then
  echo "golden-state runner must start as container root" >&2
  exit 1
fi

for command in ip nft ping sysctl unshare; do
  command -v "$command" >/dev/null
done

current_caps=$(capsh --print | sed -n 's/^Current: //p')
current_caps=${current_caps%%=*}
for capability in cap_net_admin cap_net_raw cap_sys_admin; do
  case ",$current_caps," in
    *",$capability,"*) ;;
    *)
      echo "golden-state runner requires $capability" >&2
      exit 1
      ;;
  esac
done

left=guardb3left
router=guardb3router
right=guardb3right

cleanup() {
  ip netns del "$left" 2>/dev/null || true
  ip netns del "$router" 2>/dev/null || true
  ip netns del "$right" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM
cleanup

ip netns add "$left"
ip netns add "$router"
ip netns add "$right"

ip link add g3left type veth peer name g3rleft
ip link set g3left netns "$left"
ip link set g3rleft netns "$router"
ip link add g3right type veth peer name g3rright
ip link set g3right netns "$right"
ip link set g3rright netns "$router"

ip -n "$left" link set lo up
ip -n "$left" addr add 192.0.2.2/24 dev g3left
ip -n "$left" addr add 2001:db8:1::2/64 dev g3left
ip -n "$left" link set g3left up
ip -n "$left" route add default via 192.0.2.1
ip -n "$left" -6 route add default via 2001:db8:1::1

ip -n "$router" link set lo up
ip -n "$router" addr add 192.0.2.1/24 dev g3rleft
ip -n "$router" addr add 198.51.100.1/24 dev g3rright
ip -n "$router" addr add 2001:db8:1::1/64 dev g3rleft
ip -n "$router" addr add 2001:db8:2::1/64 dev g3rright
ip -n "$router" link set g3rleft up
ip -n "$router" link set g3rright up

ip -n "$right" link set lo up
ip -n "$right" addr add 198.51.100.2/24 dev g3right
ip -n "$right" addr add 2001:db8:2::2/64 dev g3right
ip -n "$right" link set g3right up
ip -n "$right" route add default via 198.51.100.1
ip -n "$right" -6 route add default via 2001:db8:2::1

# Docker mounts /proc/sys read-only. A private mount namespace makes it writable
# only while enabling forwarding inside the disposable router network namespace.
# Cleanup removes the router and its namespaced sysctl state with the topology.
ip netns exec "$router" unshare --mount --propagation private sh -c '
  mount -o remount,rw /proc/sys
  sysctl -q -w net.ipv4.ip_forward=1 net.ipv6.conf.all.forwarding=1
'

ip netns exec "$router" nft -f - <<'NFT'
add table inet guard
add set inet guard allow_v4 { type ipv4_addr; flags interval; }
add set inet guard allow_v6 { type ipv6_addr; flags interval; }
add set inet guard protected_v4 { type ipv4_addr; flags interval; }
add set inet guard protected_v6 { type ipv6_addr; flags interval; }
add set inet guard ban_input_v4 { type ipv4_addr; flags interval,timeout; }
add set inet guard ban_input_v6 { type ipv6_addr; flags interval,timeout; }
add set inet guard ban_forward_v4 { type ipv4_addr; flags interval,timeout; }
add set inet guard ban_forward_v6 { type ipv6_addr; flags interval,timeout; }
add chain inet guard guard_policy
add rule inet guard guard_policy counter comment "guard/v1 infrastructure/v1"
add chain inet guard input { type filter hook input priority 0; policy accept; }
add chain inet guard forward { type filter hook forward priority 0; policy accept; }
add rule inet guard input ip saddr @allow_v4 return
add rule inet guard input ip saddr @protected_v4 return
add rule inet guard input ip saddr @ban_input_v4 drop
add rule inet guard input ip6 saddr @allow_v6 return
add rule inet guard input ip6 saddr @protected_v6 return
add rule inet guard input ip6 saddr @ban_input_v6 drop
add rule inet guard forward ip saddr @allow_v4 return
add rule inet guard forward ip saddr @protected_v4 return
add rule inet guard forward ip saddr @ban_forward_v4 drop
add rule inet guard forward ip6 saddr @allow_v6 return
add rule inet guard forward ip6 saddr @protected_v6 return
add rule inet guard forward ip6 saddr @ban_forward_v6 drop
NFT

ip netns exec "$router" nft add table inet guard_b3_foreign
foreign_before=$(ip netns exec "$router" nft --json list table inet guard_b3_foreign | sha256sum | awk '{print $1}')

# This batch is the provider's fixed infrastructure layout. The
# same source is allowed/protected and banned so packet outcomes prove the
# provider's allow/protected-before-ban order in both INPUT and FORWARD paths.
for set in allow_v4 ban_input_v4 ban_forward_v4; do
  ip netns exec "$router" nft add element inet guard "$set" '{ 192.0.2.2 }'
done
ip netns exec "$left" ping -c 1 -W 1 192.0.2.1 >/dev/null
ip netns exec "$left" ping -c 1 -W 1 198.51.100.2 >/dev/null
ip netns exec "$router" nft delete element inet guard allow_v4 '{ 192.0.2.2 }'
if ip netns exec "$left" ping -c 1 -W 1 192.0.2.1 >/dev/null 2>&1 ||
  ip netns exec "$left" ping -c 1 -W 1 198.51.100.2 >/dev/null 2>&1; then
  echo "IPv4 ban did not block provider INPUT/FORWARD traffic" >&2
  exit 1
fi
ip netns exec "$router" nft add element inet guard protected_v4 '{ 192.0.2.2 }'
ip netns exec "$left" ping -c 1 -W 1 192.0.2.1 >/dev/null
ip netns exec "$left" ping -c 1 -W 1 198.51.100.2 >/dev/null
ip netns exec "$router" nft delete element inet guard protected_v4 '{ 192.0.2.2 }'

for set in allow_v6 ban_input_v6 ban_forward_v6; do
  ip netns exec "$router" nft add element inet guard "$set" '{ 2001:db8:1::2 }'
done
ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:1::1 >/dev/null
ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:2::2 >/dev/null
ip netns exec "$router" nft delete element inet guard allow_v6 '{ 2001:db8:1::2 }'
if ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:1::1 >/dev/null 2>&1 ||
  ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:2::2 >/dev/null 2>&1; then
  echo "IPv6 ban did not block provider INPUT/FORWARD traffic" >&2
  exit 1
fi
ip netns exec "$router" nft add element inet guard protected_v6 '{ 2001:db8:1::2 }'
ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:1::1 >/dev/null
ip netns exec "$left" ping -6 -c 1 -W 1 2001:db8:2::2 >/dev/null
ip netns exec "$router" nft delete element inet guard protected_v6 '{ 2001:db8:1::2 }'

# nft evaluates a complete batch atomically. Duplicate create declarations make
# the batch fail and the first creation must not survive.
if ip netns exec "$router" nft -f - 2>/dev/null <<'NFT'
create table inet guard_b3_atomic
create table inet guard_b3_atomic
NFT
then
  echo "invalid nftables batch unexpectedly succeeded" >&2
  exit 1
fi
if ip netns exec "$router" nft list table inet guard_b3_atomic >/dev/null 2>&1; then
  echo "failed nftables batch left partial state" >&2
  exit 1
fi

foreign_after=$(ip netns exec "$router" nft --json list table inet guard_b3_foreign | sha256sum | awk '{print $1}')
test "$foreign_before" = "$foreign_after"

ip netns exec "$router" nft delete table inet guard
if ip netns exec "$router" nft list table inet guard >/dev/null 2>&1; then
  echo "Guard table cleanup failed" >&2
  exit 1
fi
ip netns exec "$router" nft list table inet guard_b3_foreign >/dev/null
foreign_after_cleanup=$(ip netns exec "$router" nft --json list table inet guard_b3_foreign | sha256sum | awk '{print $1}')
test "$foreign_before" = "$foreign_after_cleanup"

echo "B3_GOLDEN_STATE_PASS nft=$(ip netns exec "$router" nft --version | tr -d '\n') hook_priority=0 baseline=isolated-no-ufw-no-docker"
