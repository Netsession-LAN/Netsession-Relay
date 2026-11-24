#!/bin/sh
# OPTIONAL extra hardening of a dedicated relay host. The relay does not need it: the engine shields the host
# itself at every start (no routing on the relay interfaces, a firewall table for them, management routes).
# This script goes further, for a machine that does nothing but relay:
#
#   sh deploy/host/setup.sh <trunk-interface> [mgmt-interface]     e.g. ens19 eth0     (as root, idempotent)
#
# - sysctls: no routing at all, sane ARP on multi-VLAN hosts, IPv6 off for new (VLAN) interfaces, port 161 for
#   the SNMP agent of the web part
# - Docker daemon: no ip-forward, no iptables, no default bridge (host networking only). Do NOT use this on a
#   host that runs other containers with their own networks.
# - nftables table "nsrelay": only the MGMT interface reaches the services of the host (SSH, web UI, SNMP).
#   Every other interface - also ones the relay does not use - may only reach the host with ICMP echo and IGMP;
#   nothing is forwarded. More interfaces that shall stay open (a storage or backup NIC):
#   NSRELAY_OPEN_IFACES="ens20 ens21" sh setup.sh ...
#   (The engine keeps its own table "nsrelay_engine" next to it; a packet has to pass both.)
# - trunk interface up without address
set -eu
TRUNK="${1:?usage: setup.sh <trunk-interface> [mgmt-interface]}"
MGMT="${2:-}"
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
say() { printf '==> %s\n' "$*"; }
# The MGMT interface must be known before the firewall is written: it is the only one that stays open.
[ -n "$MGMT" ] || MGMT=$(ip -4 route show default | awk '{for (i = 1; i < NF; i++) if ($i == "dev") {print $(i + 1); exit}}')
[ -n "$MGMT" ] || { echo "no MGMT interface given and no default route found"; exit 1; }
ip link show "$MGMT" >/dev/null 2>&1 || { echo "MGMT interface $MGMT does not exist"; exit 1; }
[ "$MGMT" != "$TRUNK" ] || { echo "MGMT and trunk must be different interfaces"; exit 1; }
OPEN="\"lo\", \"$MGMT\""
for i in ${NSRELAY_OPEN_IFACES:-}; do OPEN="$OPEN, \"$i\""; done

say "sysctl"
cat > /etc/sysctl.d/90-nsrelay.conf <<EOF
# Netsession Relay: the VM must never route between game VLANs
net.ipv4.ip_forward = 0
net.ipv4.conf.all.forwarding = 0
net.ipv4.conf.all.arp_ignore = 1
net.ipv4.conf.all.arp_announce = 2
net.ipv4.conf.all.rp_filter = 0
net.ipv4.conf.default.rp_filter = 0
# interfaces created later (VLAN subinterfaces of the engine) start without IPv6
net.ipv6.conf.default.disable_ipv6 = 1
net.ipv6.conf.${TRUNK}.disable_ipv6 = 1
# more IGMP memberships per socket (one socket per VLAN, a few groups each)
net.ipv4.igmp_max_memberships = 64
# the SNMP agent of the web part (uid 10001, no capabilities) binds UDP 161 on the MGMT address
net.ipv4.ip_unprivileged_port_start = 161
EOF
sysctl -q --system

say "Docker daemon hardening (merged into an existing daemon.json)"
mkdir -p /etc/docker
[ -f /etc/docker/daemon.json ] && cp -n /etc/docker/daemon.json /etc/docker/daemon.json.before-nsrelay
python3 - <<'PY'
import json, os
p = "/etc/docker/daemon.json"
cfg = json.load(open(p)) if os.path.exists(p) and os.path.getsize(p) else {}
cfg.update({
    "ip-forward": False, "iptables": False, "ip6tables": False, "bridge": "none",
    "log-driver": "json-file", "log-opts": {"max-size": "10m", "max-file": "3"},
})
# Docker 29: keep overlay2; the containerd snapshotter lost containers after reboots on the Screens VM
cfg.setdefault("features", {})["containerd-snapshotter"] = False
json.dump(cfg, open(p, "w"), indent=2)
PY
systemctl restart docker
sysctl -q -w net.ipv4.ip_forward=0

say "trunk interface $TRUNK (no address, up)"
ip link set "$TRUNK" up
if [ -d /etc/network/interfaces.d ] && ! grep -qs "$TRUNK" /etc/network/interfaces /etc/network/interfaces.d/* 2>/dev/null; then
    printf 'auto %s\niface %s inet manual\n' "$TRUNK" "$TRUNK" > /etc/network/interfaces.d/nsrelay-trunk
fi

say "nftables"
command -v nft >/dev/null || { apt-get update -qq && apt-get install -y -qq nftables >/dev/null; }
cat > /etc/nftables.d-nsrelay.nft <<EOF
# Netsession Relay host filter (the relay itself uses AF_PACKET before netfilter and is not affected).
# Open: loopback and MGMT (SSH, web UI, SNMP agent). Every other interface is a relay interface - the trunk,
# its VLAN interfaces, interfaces attached as a device, also ones added later: participants reach the relay
# addresses there only with ping and IGMP, never the web UI.
# If MGMT itself is attached to the relay as a device (mDNS between MGMT and other networks), it stays open.
table inet nsrelay {
    chain input {
        type filter hook input priority 0; policy accept;
        iifname { ${OPEN} } accept
        icmp type echo-request accept
        ip protocol igmp accept
        ct state established,related accept
        drop
    }
    chain forward {
        type filter hook forward priority 0; policy drop;
    }
}
EOF
nft list table inet nsrelay >/dev/null 2>&1 && nft delete table inet nsrelay
nft -f /etc/nftables.d-nsrelay.nft
if ! grep -qs nftables.d-nsrelay /etc/nftables.conf; then
    printf '\ninclude "/etc/nftables.d-nsrelay.nft"\n' >> /etc/nftables.conf
fi
systemctl enable -q nftables

say "done. Check: sysctl net.ipv4.ip_forward (0), nft list table inet nsrelay"
say "MGMT interface: $MGMT (SSH, web :8080 and Zabbix only there; open interfaces: $OPEN)"
exit 0
