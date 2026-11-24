#!/bin/bash
# Build the network-namespace lab. Needs root, iproute2, kernel with 8021q + bridge.
#
#   ns relay : trunk0 (tagged 1100-1103) ── bridge labsw (vlan_filtering, plays the core switch)
#   ns relay2: second trunk for the dual-active test
#   ns gs    : eth0 access VLAN 1100, 10.10.0.10/24
#   ns t1..t3: eth0 access VLAN 1101..1103, 10.10.N.10/24
#   ns mrelay: a second relay instance like the mDNS relay: trunk0 (tagged 1101, 1102) and mgmt0, an interface
#              with an address and the default route (the management side), wired directly to ns mg
#   ns mg    : eth0 198.19.0.100/15, a host in the management network
#
# Idempotent: tears down an existing lab first.
set -euo pipefail

"$(dirname "$0")/down.sh" 2>/dev/null || true

ip link add labsw type bridge vlan_filtering 1
ip link set labsw up

ip netns add relay
ip link add trunk0 type veth peer name p-relay
ip link set trunk0 netns relay
ip link set p-relay master labsw up
bridge vlan del dev p-relay vid 1
for vid in 1100 1101 1102 1103; do bridge vlan add dev p-relay vid "$vid"; done
ip -n relay link set lo up

# second relay (dual-active test), idle unless an engine is started in it
ip netns add relay2
ip link add trunk0 type veth peer name p-relay2
ip link set trunk0 netns relay2
ip link set p-relay2 master labsw up
bridge vlan del dev p-relay2 vid 1
for vid in 1100 1101 1102 1103; do bridge vlan add dev p-relay2 vid "$vid"; done
ip -n relay2 link set lo up

client() { # name vid third
  local ns=$1 vid=$2 third=$3
  ip netns add "$ns"
  ip link add "p-$ns" type veth peer name eth0 netns "$ns"
  ip link set "p-$ns" master labsw up
  bridge vlan del dev "p-$ns" vid 1
  bridge vlan add dev "p-$ns" vid "$vid" pvid untagged
  ip -n "$ns" addr add "10.10.$third.10/24" dev eth0
  ip -n "$ns" link set eth0 up
  ip -n "$ns" link set lo up
}
client gs 1100 0
client t1 1101 1
client t2 1102 2
client t3 1103 3

# second relay instance with a management interface the relay also works on (attach: device)
ip netns add mrelay
ip link add trunk0 type veth peer name p-mrelay
ip link set trunk0 netns mrelay
ip link set p-mrelay master labsw up
bridge vlan del dev p-mrelay vid 1
for vid in 1101 1102; do bridge vlan add dev p-mrelay vid "$vid"; done
ip -n mrelay link set lo up
ip netns add mg
ip link add mgmt0 type veth peer name p-mg
ip link set mgmt0 netns mrelay
ip link set p-mg netns mg name eth0
ip -n mg addr add 198.19.0.100/15 dev eth0
ip -n mg link set eth0 up
ip -n mg link set lo up
ip -n mrelay addr add 198.18.0.56/15 dev mgmt0
ip -n mrelay link set mgmt0 up
ip -n mrelay route add default via 198.18.0.1 dev mgmt0

echo "lab up: relay(trunk0) gs t1 t2 t3, mrelay(trunk0, mgmt0) mg"
