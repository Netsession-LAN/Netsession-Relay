#!/bin/bash
# Mandatory lab cases. Expects up.sh to have run and the
# engine to be running in ns "relay". Usage: test.sh <path-to-nsrelay>
set -uo pipefail
NS=${1:-nsrelay}
fail=0
pass=0

nsx() { local ns=$1; shift; ip netns exec "$ns" "$NS" "$@"; }

# case NAME SENDER SEND-ARGS -- EXPECTATION...
# each expectation: "<ns> <expect-args>" run in background before sending
run_case() {
  local name=$1 sender=$2 send=$3; shift 3
  local pids=() logs=()
  for exp in "$@"; do
    local ns=${exp%% *} args=${exp#* }
    local log; log=$(mktemp)
    # shellcheck disable=SC2086
    nsx "$ns" lab expect -iface eth0 $args >"$log" 2>&1 &
    pids+=($!); logs+=("$ns: $log")
  done
  sleep 0.3
  # shellcheck disable=SC2086
  nsx "$sender" lab send -iface eth0 $send
  local ok=1 i=0
  for pid in "${pids[@]}"; do
    if ! wait "$pid"; then ok=0; echo "    ${logs[$i]%%:*}: $(cat "${logs[$i]#*: }")"; fi
    i=$((i+1))
  done
  if [ $ok = 1 ]; then echo "PASS  $name"; pass=$((pass+1)); else echo "FAIL  $name"; fail=$((fail+1)); fi
}

# 1. FlatOut query from t1 reaches t2, t3, gs with original source; never back into t1
run_case "FlatOut query t1 -> t2,t3,gs (original IP, not back into t1)" t1 \
  "-src 10.10.1.10 -dst 255.255.255.255 -sport 50000 -dport 23757 -payload FO2-query" \
  "t2 -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "t3 -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "gs -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "t1 -dport 23757 -src 10.10.1.10 -none -timeout 800"

# 2. FlatOut answer (106 bytes, subnet broadcast) from t2 reaches t1 as t1's subnet broadcast
run_case "FlatOut answer t2 -> t1 (subnet broadcast rewritten)" t2 \
  "-src 10.10.2.10 -dst 10.10.2.255 -sport 23757 -dport 23757 -size 106" \
  "t1 -dport 23757 -src 10.10.2.10 -dst 10.10.1.255"

# 3. Source engine query to 255.255.255.255:27015 keeps the limited broadcast (dst_mode keep)
run_case "Source query t3 -> gs (255.255.255.255 is kept)" t3 \
  "-src 10.10.3.10 -dst 255.255.255.255 -sport 27005 -dport 27015 -payload-hex ffffffff54536f7572636520456e67696e6520517565727900" \
  "gs -dport 27015 -src 10.10.3.10 -dst 255.255.255.255"

# 4. DHCP discover is never relayed
run_case "DHCP discover is never relayed" t1 \
  "-src 0.0.0.0 -dst 255.255.255.255 -sport 68 -dport 67 -size 300" \
  "t2 -dport 67 -none -timeout 800" \
  "gs -dport 67 -none -timeout 800"

# 5. Source IP from another VLAN's network (another relay) is dropped
run_case "foreign source IP (another relay) is dropped" t1 \
  "-src 10.10.2.99 -dst 255.255.255.255 -sport 50000 -dport 23757 -payload foreign" \
  "t3 -dport 23757 -src 10.10.2.99 -none -timeout 800" \
  "gs -dport 23757 -src 10.10.2.99 -none -timeout 800"

# 6. Minecraft Java LAN beacon (multicast 224.0.2.60:4445)
run_case "Minecraft multicast t1 -> t2" t1 \
  "-src 10.10.1.10 -dst 224.0.2.60 -sport 4445 -dport 4445 -payload [MOTD]lab[/MOTD][AD]25565[/AD]" \
  "t2 -dport 4445 -src 10.10.1.10 -dst 224.0.2.60"

# 7. Port without rule is not relayed
run_case "unknown port is not relayed" t1 \
  "-src 10.10.1.10 -dst 255.255.255.255 -sport 1234 -dport 4711 -payload x" \
  "t2 -dport 4711 -none -timeout 800"

# ---- protection ----
SNAP=${SNAP:-/tmp/nsrelay/active.json}
ENGINE_PID=${ENGINE_PID:-}

# 8. Rate limit: 200 frames at once from one source -> at most burst (10) per target
run_case "rate limit: 200 frames at once -> at most 10 arrive" t1   "-src 10.10.1.10 -dst 255.255.255.255 -sport 50001 -dport 27015 -size 40 -count 200"   "t2 -dport 27015 -src 10.10.1.10 -max 10 -timeout 1500"

# 9. IGMP: the relay joined the Minecraft group on every VLAN interface (IGMP snooping lets it through)
if ip -n relay maddr show dev trunk0.1102 | grep -q 224.0.2.60; then
  echo "PASS  IGMP membership 224.0.2.60 on trunk0.1102"; pass=$((pass+1))
else
  echo "FAIL  IGMP membership 224.0.2.60 missing"; ip -n relay maddr show dev trunk0.1102; fail=$((fail+1))
fi

if [ -n "$ENGINE_PID" ]; then
  # 10. observe mode via snapshot reload: counted, nothing sent
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -mode observe >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5
  run_case "mode observe: nothing leaves the relay" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50002 -dport 23757 -payload observe"     "t2 -dport 23757 -src 10.10.1.10 -none -timeout 800"
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -mode active >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

  # 11. snapshot change under load: 15 frames over ~3 s (within the rate), reload in the middle, all arrive
  ( sleep 1.2; kill -HUP "$ENGINE_PID" ) &
  run_case "snapshot change under load: no loss" t3     "-src 10.10.3.10 -dst 255.255.255.255 -sport 50003 -dport 23757 -payload load -count 15 -interval 200"     "t1 -dport 23757 -src 10.10.3.10 -count 15 -timeout 5000"
  wait

  # 12. second engine (cloned VM): the later one goes to standby, no frame arrives twice
  ip netns exec relay2 "$NS" lab snapshot -out /tmp/nsrelay/relay2.json -relay-host 56 >/dev/null
  ip netns exec relay2 "$NS" engine -snapshot /tmp/nsrelay/relay2.json -socket "" >/tmp/nsrelay/engine2.log 2>&1 &
  echo $! > /tmp/nsrelay/engine2.pid
  sleep 3
  run_case "second engine: goes to standby, no frame twice" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50004 -dport 23757 -payload dual"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"     "gs -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"
  if grep -q STANDBY /tmp/nsrelay/engine2.log; then
    echo "PASS  engine 2 reports STANDBY"; pass=$((pass+1))
  else
    echo "FAIL  engine 2 does not report STANDBY"; fail=$((fail+1))
  fi
  kill -TERM "$(cat /tmp/nsrelay/engine2.pid)"; rm -f /tmp/nsrelay/engine2.pid; sleep 0.5

  # 12b. second engine for other ports only (mDNS relay next to the game relay): both stay active, the game
  # frame still arrives once, the mDNS frame is relayed by engine 2
  ip netns exec relay2 "$NS" lab snapshot -out /tmp/nsrelay/relay2.json -relay-host 56 -games mdns >/dev/null
  ip netns exec relay2 "$NS" engine -snapshot /tmp/nsrelay/relay2.json -socket "" >/tmp/nsrelay/engine2.log 2>&1 &
  echo $! > /tmp/nsrelay/engine2.pid
  sleep 3
  run_case "mDNS instance alongside: the game frame arrives exactly once" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50005 -dport 23757 -payload mdnsside"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"
  run_case "mDNS instance relays 224.0.0.251:5353" t1     "-src 10.10.1.10 -dst 224.0.0.251 -sport 5353 -dport 5353 -payload mdns"     "t2 -dport 5353 -count 1 -max 1 -timeout 1500"
  if grep -q STANDBY /tmp/nsrelay/engine2.log; then
    echo "FAIL  mDNS instance went to STANDBY"; fail=$((fail+1))
  else
    echo "PASS  mDNS instance stays active"; pass=$((pass+1))
  fi
  kill -TERM "$(cat /tmp/nsrelay/engine2.pid)"; rm -f /tmp/nsrelay/engine2.pid; sleep 0.5

  # 12c. forged relay beacon from a participant (unsigned, "active, started long ago, all ports"): the relay
  # signs its beacons with the key of its state directory, ignores the forged one, keeps relaying and alarms
  ip netns exec t1 "$NS" lab beacon -iface eth0 -count 8 -interval 300 &
  BEACON_PID=$!
  sleep 1.2
  run_case "forged relay beacon from table 1: the relay keeps relaying" t2     "-src 10.10.2.10 -dst 255.255.255.255 -sport 50009 -dport 23757 -payload forged"     "t1 -dport 23757 -src 10.10.2.10 -count 1 -timeout 1500"
  wait "$BEACON_PID"
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'standby false'; then
    echo "PASS  forged relay beacon: no standby"; pass=$((pass+1))
  else
    echo "FAIL  forged relay beacon: the relay went to standby"; fail=$((fail+1))
  fi
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'ALARM unknown_beacon 1101'; then
    echo "PASS  forged relay beacon: alarm unknown_beacon for VLAN 1101"; pass=$((pass+1))
  else
    echo "FAIL  forged relay beacon: no alarm"; "$NS" admin status -socket /tmp/nsrelay/engine.sock; fail=$((fail+1))
  fi
  if [ "$(stat -c %a /tmp/nsrelay/beacon.key)" = 600 ]; then
    echo "PASS  beacon key beacon.key created (0600)"; pass=$((pass+1))
  else
    echo "FAIL  beacon.key missing or wrong permissions"; fail=$((fail+1))
  fi

  # 12d. storm brake: with a low limit (100 frames/s) table 1 floods the Source ports hardest. Only that rule
  # is paused and only for frames from table 1; the same game from table 2 and other games keep working.
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -storm-pps 100 >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5
  FLOOD=""
  flood() { # ns third port
    ip netns exec "$1" "$NS" lab send -iface eth0 -src "10.10.$2.10" -dst 255.255.255.255 -sport 50040 -dport "$3" -size 30 -count 70 -interval 100 &
    FLOOD="$FLOOD $!"
  }
  for port in 27015 27016 27017 27018; do flood t1 1 "$port"; done
  for port in 27015 27016; do flood t2 2 "$port"; flood t3 3 "$port"; flood gs 0 "$port"; done
  sleep 4.7
  run_case "storm brake: rule paused for the loud network (table 1)" t1     "-src 10.10.1.11 -dst 255.255.255.255 -sport 50041 -dport 27020 -payload stormpaused"     "t2 -dport 27020 -src 10.10.1.11 -none -timeout 800"
  run_case "storm brake: the same game from table 2 keeps working" t2     "-src 10.10.2.11 -dst 255.255.255.255 -sport 50042 -dport 27019 -payload stormother"     "t1 -dport 27019 -src 10.10.2.11 -count 1 -timeout 1500"
  run_case "storm brake: another game from table 1 keeps working" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50043 -dport 23757 -payload stormgame"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"
  for p in $FLOOD; do wait "$p"; done
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'ALARM storm bc:27015.*VLAN 1101'; then
    echo "PASS  storm brake: the alarm names the rule and network 1101"; pass=$((pass+1))
  else
    echo "FAIL  storm brake: alarm missing"; "$NS" admin status -socket /tmp/nsrelay/engine.sock; fail=$((fail+1))
  fi
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

  # 13. source filter (one printer for mDNS): 10.10.1.20 restricts only table 1, other tables stay open
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -sources 10.10.1.20 >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5
  run_case "source filter: allowed sender in table 1 arrives" t1     "-src 10.10.1.20 -dst 255.255.255.255 -sport 50006 -dport 23757 -payload srcok"     "t2 -dport 23757 -src 10.10.1.20 -count 1 -timeout 1500"
  run_case "source filter: another sender in table 1 is dropped" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50007 -dport 23757 -payload srcdrop"     "t2 -dport 23757 -src 10.10.1.10 -none -timeout 800"
  run_case "source filter: table 2 without an entry stays open" t2     "-src 10.10.2.10 -dst 255.255.255.255 -sport 50008 -dport 23757 -payload srcopen"     "t1 -dport 23757 -src 10.10.2.10 -count 1 -timeout 1500"
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

  # 13b. an interface that is recreated under the same name gets a new ifindex: the engine opens a new socket,
  # stops the receiver of the old one and closes its socket (no leak), and the network is relayed again
  engine_socks() { ls -l "/proc/$ENGINE_PID/fd" | grep -c 'socket:'; }
  SOCKS_BEFORE=$(engine_socks)
  ip -n relay link del trunk0.1102
  kill -HUP "$ENGINE_PID"; sleep 1.2
  run_case "interface recreated under the same name: the network is relayed again" t2 \
    "-src 10.10.2.10 -dst 255.255.255.255 -sport 50060 -dport 23757 -payload recreated" \
    "t1 -dport 23757 -src 10.10.2.10 -count 1 -timeout 1500"
  if [ "$(engine_socks)" = "$SOCKS_BEFORE" ]; then
    echo "PASS  interface recreated: old socket closed ($SOCKS_BEFORE sockets as before)"; pass=$((pass+1))
  else
    echo "FAIL  interface recreated: $(engine_socks) sockets instead of $SOCKS_BEFORE"; ls -l "/proc/$ENGINE_PID/fd"; fail=$((fail+1))
  fi

  check() { # name command...
    local name=$1; shift
    if out=$("$@" 2>&1); then echo "PASS  $name"; pass=$((pass+1)); else echo "FAIL  $name"; echo "$out" | sed 's/^/    /'; fail=$((fail+1)); fi
  }

  # ---- host protection: the engine shields the host itself, no setup script (hostprep) ----
  # 14. per relay interface: no routing, ARP only for the own address, no IPv6; nftables table of the engine
  STATUS="\"$NS\" admin status -socket /tmp/nsrelay/engine.sock"
  rconf() { ip netns exec relay cat "/proc/sys/net/ipv4/conf/$1/$2"; }
  rnft() { ip netns exec relay nft "$@"; }
  reapply() { kill -HUP "$ENGINE_PID"; sleep 0.7; }
  dial() { ip netns exec "$1" "$NS" lab tcp-dial -addr "$2" -timeout 1200; }
  nodial() { ! dial "$@"; }
  hardened() { [ "$(rconf "$1" forwarding)" = 0 ] && [ "$(rconf "$1" arp_ignore)" = 1 ] && [ "$(rconf "$1" arp_announce)" = 2 ] && [ "$(rconf "$1" rp_filter)" = 0 ]; }
  check "host: the engine reports the preparation as complete" sh -c "$STATUS | grep -q 'host prepared'"
  all_hardened() { local i; for i in trunk0 trunk0.1100 trunk0.1101 trunk0.1102 trunk0.1103; do hardened "$i" || { echo "$i is not hardened"; return 1; }; done; }
  check "host: relay interfaces without routing, ARP only for the own address" all_hardened
  check "host: no IPv6 on the relay interfaces" sh -c "! ip -n relay -6 addr show | grep -q inet6.*fe80"
  nft_ifaces() { [ "$(rnft list set inet nsrelay_engine relay_if | grep -o '"trunk0[.0-9]*"' | sort -u | wc -l)" = 5 ] || { rnft list set inet nsrelay_engine relay_if; return 1; }; }
  check "host: the nftables table of the engine names the trunk and the four VLAN interfaces" nft_ifaces

  # 15. the host forwards IPv4 globally (Docker switches that on), a participant uses the relay as a router and
  # tries its services. First the counter-check without protection, then each layer alone, then both.
  ip -n t1 route add 10.10.2.0/24 via 10.10.1.55
  ip -n t2 route add 10.10.1.0/24 via 10.10.2.55
  ip netns exec relay sh -c 'echo 1 > /proc/sys/net/ipv4/ip_forward'
  unshield() { # what the host would be without the engine: routing on the relay interfaces, no firewall
    rnft delete table inet nsrelay_engine
    for i in trunk0.1101 trunk0.1102; do ip netns exec relay sh -c "echo 1 > /proc/sys/net/ipv4/conf/$i/forwarding"; done
  }
  ip netns exec t2 "$NS" lab tcp-listen -addr 10.10.2.10:2222 -timeout 30000 >/dev/null 2>&1 &
  LISTEN_T2=$!
  ip netns exec relay "$NS" lab tcp-listen -addr 0.0.0.0:2222 -timeout 30000 >/dev/null 2>&1 & # stands for SSH on the host
  LISTEN_HOST=$!
  sleep 0.3
  unshield
  check "counter-check without protection: the host routes table 1 -> table 2" dial t1 10.10.2.10:2222
  check "counter-check without protection: host service reachable through the relay address" dial t1 10.10.1.55:2222
  reapply
  check "host: completely prepared again after another apply" all_hardened
  check "host: the firewall table is back" rnft list table inet nsrelay_engine
  check "the host forwards IPv4 globally (as with Docker)" sh -c '[ "$(ip netns exec relay cat /proc/sys/net/ipv4/ip_forward)" = 1 ]'
  check "ip_forward=1: nothing is routed between two relay networks" nodial t1 10.10.2.10:2222
  check "host service (like SSH) not reachable through the relay address" nodial t1 10.10.1.55:2222
  check "host service stays reachable locally" dial relay 127.0.0.1:2222
  check "ping to the relay address still works" ip netns exec t1 ping -c 1 -W 2 10.10.1.55
  rnft delete table inet nsrelay_engine
  check "interface settings alone (without the firewall): no routing" nodial t1 10.10.2.10:2222
  reapply
  for i in trunk0.1101 trunk0.1102; do ip netns exec relay sh -c "echo 1 > /proc/sys/net/ipv4/conf/$i/forwarding"; done
  check "firewall alone (routing on the interfaces on): no routing" nodial t1 10.10.2.10:2222
  reapply
  run_case "the relay keeps working with the host protection" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50050 -dport 23757 -payload shielded"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"

  # 16. management side: a second interface with the default route. It is never shielded, and answers from its
  # address to a relayed network leave through its gateway (no asymmetric routing).
  ip -n relay link add mgmt0 type veth peer name mgmt0p
  ip -n relay addr add 192.0.2.10/24 dev mgmt0
  ip -n relay link set mgmt0 up
  ip -n relay link set mgmt0p up
  ip -n relay route add default via 192.0.2.1 dev mgmt0
  reapply
  check "management: answers from the management address into a relay network leave through the management gateway" sh -c "ip -n relay route get 10.10.1.10 from 192.0.2.10 | grep -q 'via 192.0.2.1 dev mgmt0'"
  check "management: other traffic into the relay network stays direct" sh -c "ip -n relay route get 10.10.1.10 | grep -q 'dev trunk0.1101'"
  check "management interface is not in the firewall of the engine" sh -c "! ip netns exec relay nft list table inet nsrelay_engine | grep -q mgmt0"
  check "management: the engine still reports complete" sh -c "$STATUS | grep -q 'host prepared'"
  ip -n relay link del mgmt0
  reapply
  check "management rules disappear with the default route" sh -c "! ip -n relay rule show | grep -q 7355"
  kill "$LISTEN_T2" "$LISTEN_HOST" 2>/dev/null
  ip netns exec relay sh -c 'echo 0 > /proc/sys/net/ipv4/ip_forward'
  ip -n t1 route del 10.10.2.0/24
  ip -n t2 route del 10.10.1.0/24

  # 16a. a network whose interface is down: its copy cannot be sent and is counted (send_error), the other
  # networks still get theirs
  ip -n relay link set trunk0.1103 down
  run_case "interface down: the other networks still get their copy" t1 \
    "-src 10.10.1.10 -dst 255.255.255.255 -sport 50060 -dport 23757 -payload ifdown" \
    "t2 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500" \
    "gs -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"
  check "interface down: the copy that could not be sent is counted as send_error" sh -c "$STATUS | grep 'reasons' | grep -q 'send_error:'"
  ip -n relay link set trunk0.1103 up
  sleep 1.2 # the receiver of that interface pauses a second after the read error of the link going down
  run_case "interface up again: the network gets its copy again" t1 \
    "-src 10.10.1.10 -dst 255.255.255.255 -sport 50061 -dport 23757 -payload ifup" \
    "t3 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"

  # ---- second relay instance like the mDNS relay ----
  # 16b. Two networks on the trunk and a third one attached as a device: the interface that carries the address
  # and the default route of the host (management), allowed with NSRELAY_ALLOWED_IFACES. Only mDNS is on
  # (multicast 224.0.0.251:5353, copies leave with the relay address) with a source filter for the first
  # network. The engine starts like in the container (read-only /proc/sys), is configured through the database
  # like the web part does it, and runs next to the main relay with its own beacon key. Multicast has to arrive
  # from every network in the other two - also from and to the management interface - and still after a
  # restart from the stored snapshot.
  MD=/tmp/nsrelay/mdns
  mkdir -p "$MD"
  cat > "$MD/config.yaml" <<'YAML'
version: 1
event: mDNS
mode: active
vlans:
  - {vid: 1101, name: Orga, group: orga, cidr: 10.10.1.0/24, relay_ip: 10.10.1.56, attach: trunk, parent: trunk0, enabled: true}
  - {vid: 1102, name: Gaeste, group: gaeste, cidr: 10.10.2.0/24, relay_ip: 10.10.2.56, attach: trunk, parent: trunk0, enabled: true}
  - {vid: 4000, name: MGMT, group: mgmt, cidr: 198.18.0.0/15, relay_ip: 198.18.0.56, attach: device, device: mgmt0, enabled: true}
profiles:
  - name: mdns
    default: true
    edges:
      - {src: orga, dst: mgmt}
      - {src: mgmt, dst: orga}
      - {src: orga, dst: gaeste}
      - {src: gaeste, dst: orga}
      - {src: mgmt, dst: gaeste}
      - {src: gaeste, dst: mgmt}
games:
  - {id: mdns, sources: [10.10.1.20]}
YAML
  madmin() { NSRELAY_DB="$MD/relay.db" NSRELAY_SOCKET="$MD/engine.sock" "$NS" admin "$@"; }
  mengine() {
    NSRELAY_ALLOWED_IFACES=mgmt0,trunk0 ip netns exec mrelay unshare -m sh -c \
      "mount --bind /proc/sys /proc/sys && mount -o remount,ro,bind /proc/sys && exec '$NS' engine -snapshot $MD/active.json -socket $MD/engine.sock" >>"$MD/engine.log" 2>&1 &
    echo $! > /tmp/nsrelay/engine-mdns.pid
  }
  mdns_setup() { madmin init -preset lab -parent trunk0 && madmin import "$MD/config.yaml" && madmin apply -note "mDNS instance"; }
  mdns_send="-dst 224.0.0.251 -sport 5353 -dport 5353 -payload"
  mdns_cases() { # suffix for the case names, payload suffix (a restarted engine must not see "the same" datagram)
    run_case "mDNS instance$1: from the management interface into both trunk networks (relay address as sender)" mg \
      "-src 198.19.0.100 $mdns_send from-mgmt$2" \
      "t1 -dport 5353 -src 10.10.1.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "t2 -dport 5353 -src 10.10.2.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -none -timeout 800"
    run_case "mDNS instance$1: from one trunk network into the other and onto the management interface" t2 \
      "-src 10.10.2.10 $mdns_send from-guests$2" \
      "t1 -dport 5353 -src 10.10.1.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500"
    run_case "mDNS instance$1: allowed sender in the filtered network arrives in both others" t1 \
      "-src 10.10.1.20 $mdns_send from-printer$2" \
      "t2 -dport 5353 -src 10.10.2.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500"
    run_case "mDNS instance$1: another sender in the filtered network is dropped" t1 \
      "-src 10.10.1.10 $mdns_send from-other$2" \
      "t2 -dport 5353 -src 10.10.2.56 -none -timeout 800" \
      "mg -dport 5353 -src 198.18.0.56 -none -timeout 800"
  }
  mdns_joined() { local i; for i in mgmt0 trunk0.1101 trunk0.1102; do ip -n mrelay maddr show dev "$i" | grep -q 'inet  *224.0.0.251' || { echo "$i has not joined 224.0.0.251"; ip -n mrelay maddr show dev "$i"; return 1; }; done; }
  mengine
  sleep 1
  check "mDNS instance: apply the configuration (two trunk networks, management interface as a device)" mdns_setup
  sleep 2 # start holdoff
  check "mDNS instance: joined group 224.0.0.251 on all three interfaces" mdns_joined
  mdns_cases "" 1
  check "mDNS instance: counts the multicast frames (relayed 3, src_filter 1)" sh -c "NSRELAY_SOCKET=$MD/engine.sock \"$NS\" admin status | grep 'reasons' | grep 'relayed:3' | grep -q 'src_filter:1'"
  mdns_status() { # every interface counts what arrived on it (MGMT 1, trunk networks 2 and 1), the rule saw 3 frames
    madmin status > "$MD/status.txt"
    grep -q 'vlan 4000 MGMT (mgmt0): in 1, out 2,' "$MD/status.txt" && grep -q 'vlan 1101 Orga (trunk0.1101): in 2, out 2,' "$MD/status.txt" &&
      grep -q 'vlan 1102 Gaeste (trunk0.1102): in 1, out 2,' "$MD/status.txt" && grep -q '^rule .*: rx 3, relayed 3' "$MD/status.txt" || { cat "$MD/status.txt"; return 1; }
  }
  check "mDNS instance: the status names the input per interface and the rule" mdns_status
  check "mDNS instance: the management interface is not in the firewall of the engine, the trunk networks are" sh -c "ip netns exec mrelay nft list set inet nsrelay_engine relay_if | grep -q trunk0.1101 && ! ip netns exec mrelay nft list table inet nsrelay_engine | grep -q mgmt0"
  check "mDNS instance: the main relay next to it stays active" sh -c "$STATUS | grep -q 'standby false'"
  kill -TERM "$(cat /tmp/nsrelay/engine-mdns.pid)"; sleep 0.5
  mengine
  sleep 2.5
  check "mDNS instance after a restart: group joined again" mdns_joined
  mdns_cases " nach Neustart" 2
  check "mDNS instance: host preparation complete, also with a read-only /proc/sys" sh -c "NSRELAY_SOCKET=$MD/engine.sock \"$NS\" admin status | grep -q 'host prepared'"
  kill -TERM "$(cat /tmp/nsrelay/engine-mdns.pid)"; rm -f /tmp/nsrelay/engine-mdns.pid; sleep 0.5

  # ---- control plane (store, engine API, revisions, rollback, backup) ----
  export NSRELAY_DB=/tmp/nsrelay/relay.db NSRELAY_SOCKET=/tmp/nsrelay/engine.sock
  flatout() { # expectation for t2: "" (arrives) or "-none"
    run_case "$1" t1 "-src 10.10.1.10 -dst 255.255.255.255 -sport $2 -dport 23757 -payload p4"       "t2 -dport 23757 -src 10.10.1.10 $3 -timeout 1000"
  }
  check "admin init -preset lab" "$NS" admin init -preset lab -parent trunk0
  check "admin apply (Revision 1)" "$NS" admin apply -note "lab start"
  check "admin status through the socket" sh -c "\"$NS\" admin status | grep -q 'mode active'"
  # 17. web UI: reachable on the host, never through a relay address - first the firewall stops it, without the
  # firewall the web part itself closes such connections
  ip netns exec relay "$NS" web -addr :8080 >/tmp/nsrelay/web.log 2>&1 &
  echo $! > /tmp/nsrelay/web.pid
  sleep 2
  check "web: reachable on the host" ip netns exec relay "$NS" health -url http://127.0.0.1:8080/healthz
  check "web: not reachable through the relay address (firewall)" nodial t1 10.10.1.55:8080
  rnft delete table inet nsrelay_engine
  check "web without the firewall: the connection arrives but is closed without an answer" sh -c "ip netns exec t1 \"$NS\" lab tcp-dial -addr 10.10.1.55:8080 && ! ip netns exec t1 \"$NS\" health -url http://10.10.1.55:8080/healthz"
  check "web: the log names the refused relay address" grep -q "10.10.1.55" /tmp/nsrelay/web.log
  reapply
  kill -TERM "$(cat /tmp/nsrelay/web.pid)"; rm -f /tmp/nsrelay/web.pid
  check "admin disable flatout1 flatout2" "$NS" admin disable flatout1 flatout2
  check "admin apply (Revision 2)" "$NS" admin apply -note "flatout off"
  flatout "FlatOut off by CLI -> not relayed" 50010 "-none"
  check "admin enable flatout1 flatout2 + apply (Revision 3)" sh -c "\"$NS\" admin enable flatout1 flatout2 && \"$NS\" admin apply -note 'flatout on'"
  flatout "FlatOut on by CLI -> relayed" 50011 ""
  check "admin rollback 2 (Revision 4)" "$NS" admin rollback 2
  flatout "rollback to revision 2 -> FlatOut off again" 50012 "-none"
  check "admin rollback 3 (Revision 5)" "$NS" admin rollback 3
  check "admin revisions lists 5 revisions" sh -c "\"$NS\" admin revisions | grep -c '^[0-9]' | grep -qx 5"
  # port finder: session on table 1 only, unknown port 41234 shows up, table 2 is filtered out
  check "finder-start on table 1" "$NS" admin finder-start -vlans 1101 -seconds 30
  ip netns exec t1 "$NS" lab send -iface eth0 -src 10.10.1.10 -dst 255.255.255.255 -sport 50030 -dport 41234 -size 20 -count 3
  ip netns exec t2 "$NS" lab send -iface eth0 -src 10.10.2.10 -dst 255.255.255.255 -sport 50030 -dport 41235 -size 20 -count 3
  sleep 0.3
  check "finder shows port 41234 as unknown" sh -c "\"$NS\" admin finder | grep -q '41234  UNKNOWN'"
  check "finder ignores table 2" sh -c "! \"$NS\" admin finder | grep -q 41235"
  check "finder-pcap writes the capture" sh -c "\"$NS\" admin finder-pcap /tmp/nsrelay/finder.pcap && [ \$(stat -c %s /tmp/nsrelay/finder.pcap) -gt 150 ]"
  check "finder-stop" "$NS" admin finder-stop
  # server probe: a Source server in gs only answers; the relay asks from 10.10.0.x and lists it with name
  ip netns exec gs "$NS" lab a2s-server -iface eth0 -ip 10.10.0.10 -name "Lab CS2" -timeout 4000 >/tmp/nsrelay/a2s.log 2>&1 &
  A2S_PID=$!
  sleep 0.3
  check "server probe finds the CS2 server in gs (name, map, players)" sh -c "\"$NS\" admin probe | grep -q '10.10.0.10:27015.*\"Lab CS2\" \"de_lab\" 3/16'"
  check "the probe does not show up as a player (the relay address does not count)" sh -c "! \"$NS\" admin probe | grep -q '\.1:'"
  wait "$A2S_PID" || true
  check "backup writes .db and .yaml" sh -c "\"$NS\" backup -dir /tmp/nsrelay/backups -keep 3 && ls /tmp/nsrelay/backups/*.db /tmp/nsrelay/backups/*.yaml"

  # engine restarts without web and database from active.json (written by the last apply)
  kill -TERM "$ENGINE_PID"; rm -f /tmp/nsrelay/engine.pid; sleep 0.5
  mv "$NSRELAY_DB" "$NSRELAY_DB.away"
  ip netns exec relay "$NS" engine -snapshot "$SNAP" -socket /tmp/nsrelay/engine.sock >/tmp/nsrelay/engine-restart.log 2>&1 &
  echo $! > /tmp/nsrelay/engine-restart.pid
  sleep 2.5
  flatout "engine restart without web and database from active.json -> FlatOut works" 50013 ""
  mv "$NSRELAY_DB.away" "$NSRELAY_DB"

  # first apply while broadcasts flow: receivers run before the first state exists, a frame in that moment must be skipped
  kill -TERM "$(cat /tmp/nsrelay/engine-restart.pid)"; rm -f /tmp/nsrelay/engine-restart.pid "$SNAP"; sleep 0.5
  ip netns exec relay "$NS" engine -snapshot "$SNAP" -socket /tmp/nsrelay/engine.sock >/tmp/nsrelay/engine-first.log 2>&1 &
  echo $! > /tmp/nsrelay/engine-first.pid
  sleep 1
  SENDERS=""
  for c in gs:0 t1:1 t2:2 t3:3; do # flood every VLAN without pause while the first state is built
    ip netns exec "${c%%:*}" "$NS" lab send -iface eth0 -src "10.10.${c##*:}.10" -dst 255.255.255.255 -sport 50020 -dport 27015 -size 30 -count 20000 &
    SENDERS="$SENDERS $!"
  done
  sleep 0.2
  check "first apply under running broadcast traffic" "$NS" admin apply -note "first apply under traffic"
  for p in $SENDERS; do wait "$p"; done
  check "the engine is alive and active afterwards" sh -c "\"$NS\" admin status | grep -q 'mode active'"
  check "no panic in the engine log" sh -c "! grep -q panic /tmp/nsrelay/engine-first.log"
fi

echo
echo "lab: $pass passed, $fail failed"
[ $fail = 0 ]
