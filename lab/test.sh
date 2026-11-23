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
run_case "FlatOut-Anfrage t1 -> t2,t3,gs (Original-IP, nicht zurück nach t1)" t1 \
  "-src 10.10.1.10 -dst 255.255.255.255 -sport 50000 -dport 23757 -payload FO2-query" \
  "t2 -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "t3 -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "gs -dport 23757 -src 10.10.1.10 -dst 255.255.255.255" \
  "t1 -dport 23757 -src 10.10.1.10 -none -timeout 800"

# 2. FlatOut answer (106 bytes, subnet broadcast) from t2 reaches t1 as t1's subnet broadcast
run_case "FlatOut-Antwort t2 -> t1 (Subnetz-Broadcast umgeschrieben)" t2 \
  "-src 10.10.2.10 -dst 10.10.2.255 -sport 23757 -dport 23757 -size 106" \
  "t1 -dport 23757 -src 10.10.2.10 -dst 10.10.1.255"

# 3. Source engine query to 255.255.255.255:27015 keeps the limited broadcast (dst_mode keep)
run_case "Source-Anfrage t3 -> gs (255.255.255.255 bleibt)" t3 \
  "-src 10.10.3.10 -dst 255.255.255.255 -sport 27005 -dport 27015 -payload-hex ffffffff54536f7572636520456e67696e6520517565727900" \
  "gs -dport 27015 -src 10.10.3.10 -dst 255.255.255.255"

# 4. DHCP discover is never relayed
run_case "DHCP-Discover wird nie weitergeleitet" t1 \
  "-src 0.0.0.0 -dst 255.255.255.255 -sport 68 -dport 67 -size 300" \
  "t2 -dport 67 -none -timeout 800" \
  "gs -dport 67 -none -timeout 800"

# 5. Source IP from another VLAN's network (another relay) is dropped
run_case "Fremde Quell-IP (anderes Relay) wird verworfen" t1 \
  "-src 10.10.2.99 -dst 255.255.255.255 -sport 50000 -dport 23757 -payload foreign" \
  "t3 -dport 23757 -src 10.10.2.99 -none -timeout 800" \
  "gs -dport 23757 -src 10.10.2.99 -none -timeout 800"

# 6. Minecraft Java LAN beacon (multicast 224.0.2.60:4445)
run_case "Minecraft-Multicast t1 -> t2" t1 \
  "-src 10.10.1.10 -dst 224.0.2.60 -sport 4445 -dport 4445 -payload [MOTD]lab[/MOTD][AD]25565[/AD]" \
  "t2 -dport 4445 -src 10.10.1.10 -dst 224.0.2.60"

# 7. Port without rule is not relayed
run_case "Unbekannter Port wird nicht weitergeleitet" t1 \
  "-src 10.10.1.10 -dst 255.255.255.255 -sport 1234 -dport 4711 -payload x" \
  "t2 -dport 4711 -none -timeout 800"

# ---- phase 3: protection ----
SNAP=${SNAP:-/tmp/nsrelay/active.json}
ENGINE_PID=${ENGINE_PID:-}

# 8. Rate limit: 200 frames at once from one source -> at most burst (10) per target
run_case "Rate-Limit: 200 Pakete auf einmal -> höchstens 10 kommen an" t1   "-src 10.10.1.10 -dst 255.255.255.255 -sport 50001 -dport 27015 -size 40 -count 200"   "t2 -dport 27015 -src 10.10.1.10 -max 10 -timeout 1500"

# 9. IGMP: the relay joined the Minecraft group on every VLAN interface (IGMP snooping lets it through)
if ip -n relay maddr show dev trunk0.1102 | grep -q 224.0.2.60; then
  echo "PASS  IGMP-Mitgliedschaft 224.0.2.60 auf trunk0.1102"; pass=$((pass+1))
else
  echo "FAIL  IGMP-Mitgliedschaft 224.0.2.60 fehlt"; ip -n relay maddr show dev trunk0.1102; fail=$((fail+1))
fi

if [ -n "$ENGINE_PID" ]; then
  # 10. observe mode via snapshot reload: counted, nothing sent
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -mode observe >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5
  run_case "Betriebsart observe: nichts verlässt das Relay" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50002 -dport 23757 -payload observe"     "t2 -dport 23757 -src 10.10.1.10 -none -timeout 800"
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -mode active >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

  # 11. snapshot change under load: 15 frames over ~3 s (within the rate), reload in the middle, all arrive
  ( sleep 1.2; kill -HUP "$ENGINE_PID" ) &
  run_case "Snapshot-Wechsel unter Last: keine Verluste" t3     "-src 10.10.3.10 -dst 255.255.255.255 -sport 50003 -dport 23757 -payload load -count 15 -interval 200"     "t1 -dport 23757 -src 10.10.3.10 -count 15 -timeout 5000"
  wait

  # 12. second engine (cloned VM): the later one goes to standby, no frame arrives twice
  ip netns exec relay2 "$NS" lab snapshot -out /tmp/nsrelay/relay2.json -relay-host 56 >/dev/null
  ip netns exec relay2 "$NS" engine -snapshot /tmp/nsrelay/relay2.json -socket "" >/tmp/nsrelay/engine2.log 2>&1 &
  echo $! > /tmp/nsrelay/engine2.pid
  sleep 3
  run_case "Zweite Engine: geht in standby, kein Paket doppelt" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50004 -dport 23757 -payload dual"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"     "gs -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"
  if grep -q STANDBY /tmp/nsrelay/engine2.log; then
    echo "PASS  Engine 2 meldet STANDBY"; pass=$((pass+1))
  else
    echo "FAIL  Engine 2 meldet kein STANDBY"; fail=$((fail+1))
  fi
  kill -TERM "$(cat /tmp/nsrelay/engine2.pid)"; rm -f /tmp/nsrelay/engine2.pid; sleep 0.5

  # 12b. second engine for other ports only (mDNS relay next to the game relay): both stay active, the game
  # frame still arrives once, the mDNS frame is relayed by engine 2
  ip netns exec relay2 "$NS" lab snapshot -out /tmp/nsrelay/relay2.json -relay-host 56 -games mdns >/dev/null
  ip netns exec relay2 "$NS" engine -snapshot /tmp/nsrelay/relay2.json -socket "" >/tmp/nsrelay/engine2.log 2>&1 &
  echo $! > /tmp/nsrelay/engine2.pid
  sleep 3
  run_case "mDNS-Instanz daneben: Spielpaket kommt genau einmal" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50005 -dport 23757 -payload mdnsside"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -max 1 -timeout 1500"
  run_case "mDNS-Instanz leitet 224.0.0.251:5353 weiter" t1     "-src 10.10.1.10 -dst 224.0.0.251 -sport 5353 -dport 5353 -payload mdns"     "t2 -dport 5353 -count 1 -max 1 -timeout 1500"
  if grep -q STANDBY /tmp/nsrelay/engine2.log; then
    echo "FAIL  mDNS-Instanz ging in STANDBY"; fail=$((fail+1))
  else
    echo "PASS  mDNS-Instanz bleibt aktiv"; pass=$((pass+1))
  fi
  kill -TERM "$(cat /tmp/nsrelay/engine2.pid)"; rm -f /tmp/nsrelay/engine2.pid; sleep 0.5

  # 12c. forged relay beacon from a participant (unsigned, "active, started long ago, all ports"): the relay
  # signs its beacons with the key of its state directory, ignores the forged one, keeps relaying and alarms
  ip netns exec t1 "$NS" lab beacon -iface eth0 -count 8 -interval 300 &
  BEACON_PID=$!
  sleep 1.2
  run_case "Gefälschtes Relay-Signal aus Tisch 1: Relay leitet weiter" t2     "-src 10.10.2.10 -dst 255.255.255.255 -sport 50009 -dport 23757 -payload forged"     "t1 -dport 23757 -src 10.10.2.10 -count 1 -timeout 1500"
  wait "$BEACON_PID"
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'standby false'; then
    echo "PASS  Gefälschtes Relay-Signal: kein Standby"; pass=$((pass+1))
  else
    echo "FAIL  Gefälschtes Relay-Signal: Relay ging in Standby"; fail=$((fail+1))
  fi
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'ALARM unknown_beacon 1101'; then
    echo "PASS  Gefälschtes Relay-Signal: Alarm unknown_beacon für VLAN 1101"; pass=$((pass+1))
  else
    echo "FAIL  Gefälschtes Relay-Signal: kein Alarm"; "$NS" admin status -socket /tmp/nsrelay/engine.sock; fail=$((fail+1))
  fi
  if [ "$(stat -c %a /tmp/nsrelay/beacon.key)" = 600 ]; then
    echo "PASS  Signal-Schlüssel beacon.key angelegt (0600)"; pass=$((pass+1))
  else
    echo "FAIL  beacon.key fehlt oder falsche Rechte"; fail=$((fail+1))
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
  run_case "Sturm-Bremse: Regel für das laute Netz (Tisch 1) pausiert" t1     "-src 10.10.1.11 -dst 255.255.255.255 -sport 50041 -dport 27020 -payload stormpaused"     "t2 -dport 27020 -src 10.10.1.11 -none -timeout 800"
  run_case "Sturm-Bremse: dasselbe Spiel aus Tisch 2 läuft weiter" t2     "-src 10.10.2.11 -dst 255.255.255.255 -sport 50042 -dport 27019 -payload stormother"     "t1 -dport 27019 -src 10.10.2.11 -count 1 -timeout 1500"
  run_case "Sturm-Bremse: anderes Spiel aus Tisch 1 läuft weiter" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50043 -dport 23757 -payload stormgame"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"
  for p in $FLOOD; do wait "$p"; done
  if "$NS" admin status -socket /tmp/nsrelay/engine.sock | grep -q 'ALARM storm bc:27015.*VLAN 1101'; then
    echo "PASS  Sturm-Bremse: Alarm nennt Regel und Netz 1101"; pass=$((pass+1))
  else
    echo "FAIL  Sturm-Bremse: Alarm fehlt"; "$NS" admin status -socket /tmp/nsrelay/engine.sock; fail=$((fail+1))
  fi
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

  # 13. source filter (one printer for mDNS): 10.10.1.20 restricts only table 1, other tables stay open
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" -sources 10.10.1.20 >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5
  run_case "Absender-Filter: erlaubter Absender in Tisch 1 kommt an" t1     "-src 10.10.1.20 -dst 255.255.255.255 -sport 50006 -dport 23757 -payload srcok"     "t2 -dport 23757 -src 10.10.1.20 -count 1 -timeout 1500"
  run_case "Absender-Filter: anderer Absender in Tisch 1 wird verworfen" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50007 -dport 23757 -payload srcdrop"     "t2 -dport 23757 -src 10.10.1.10 -none -timeout 800"
  run_case "Absender-Filter: Tisch 2 ohne Eintrag bleibt offen" t2     "-src 10.10.2.10 -dst 255.255.255.255 -sport 50008 -dport 23757 -payload srcopen"     "t1 -dport 23757 -src 10.10.2.10 -count 1 -timeout 1500"
  ip netns exec relay "$NS" lab snapshot -out "$SNAP" >/dev/null
  kill -HUP "$ENGINE_PID"; sleep 0.5

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
  check "Host: Engine meldet die Vorbereitung als vollständig" sh -c "$STATUS | grep -q 'host prepared'"
  all_hardened() { local i; for i in trunk0 trunk0.1100 trunk0.1101 trunk0.1102 trunk0.1103; do hardened "$i" || { echo "$i is not hardened"; return 1; }; done; }
  check "Host: Relay-Schnittstellen ohne Routing, ARP nur für die eigene Adresse" all_hardened
  check "Host: kein IPv6 auf den Relay-Schnittstellen" sh -c "! ip -n relay -6 addr show | grep -q inet6.*fe80"
  nft_ifaces() { [ "$(rnft list set inet nsrelay_engine relay_if | grep -o '"trunk0[.0-9]*"' | sort -u | wc -l)" = 5 ] || { rnft list set inet nsrelay_engine relay_if; return 1; }; }
  check "Host: nftables-Tabelle der Engine nennt den Trunk und die vier VLAN-Schnittstellen" nft_ifaces

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
  check "Gegenprobe ohne Schutz: der Host routet Tisch 1 -> Tisch 2" dial t1 10.10.2.10:2222
  check "Gegenprobe ohne Schutz: Host-Dienst über die Relay-Adresse erreichbar" dial t1 10.10.1.55:2222
  reapply
  check "Host: nach erneutem Anwenden wieder vollständig vorbereitet" all_hardened
  check "Host: Firewall-Tabelle ist wieder da" rnft list table inet nsrelay_engine
  check "Der Host leitet IPv4 global weiter (wie mit Docker)" sh -c '[ "$(ip netns exec relay cat /proc/sys/net/ipv4/ip_forward)" = 1 ]'
  check "ip_forward=1: zwischen zwei Relay-Netzen wird nichts geroutet" nodial t1 10.10.2.10:2222
  check "Host-Dienst (wie SSH) über die Relay-Adresse nicht erreichbar" nodial t1 10.10.1.55:2222
  check "Host-Dienst bleibt lokal erreichbar" dial relay 127.0.0.1:2222
  check "Ping auf die Relay-Adresse geht weiter" ip netns exec t1 ping -c 1 -W 2 10.10.1.55
  rnft delete table inet nsrelay_engine
  check "Nur die Schnittstellen-Einstellung (ohne Firewall): kein Routing" nodial t1 10.10.2.10:2222
  reapply
  for i in trunk0.1101 trunk0.1102; do ip netns exec relay sh -c "echo 1 > /proc/sys/net/ipv4/conf/$i/forwarding"; done
  check "Nur die Firewall (Routing an den Schnittstellen an): kein Routing" nodial t1 10.10.2.10:2222
  reapply
  run_case "Relay arbeitet mit Host-Schutz weiter" t1     "-src 10.10.1.10 -dst 255.255.255.255 -sport 50050 -dport 23757 -payload shielded"     "t2 -dport 23757 -src 10.10.1.10 -count 1 -timeout 1500"

  # 16. management side: a second interface with the default route. It is never shielded, and answers from its
  # address to a relayed network leave through its gateway (no asymmetric routing).
  ip -n relay link add mgmt0 type veth peer name mgmt0p
  ip -n relay addr add 192.0.2.10/24 dev mgmt0
  ip -n relay link set mgmt0 up
  ip -n relay link set mgmt0p up
  ip -n relay route add default via 192.0.2.1 dev mgmt0
  reapply
  check "MGMT: Antworten von der MGMT-Adresse in ein Relay-Netz gehen über das MGMT-Gateway" sh -c "ip -n relay route get 10.10.1.10 from 192.0.2.10 | grep -q 'via 192.0.2.1 dev mgmt0'"
  check "MGMT: anderer Verkehr ins Relay-Netz bleibt direkt" sh -c "ip -n relay route get 10.10.1.10 | grep -q 'dev trunk0.1101'"
  check "MGMT-Schnittstelle steht nicht in der Firewall der Engine" sh -c "! ip netns exec relay nft list table inet nsrelay_engine | grep -q mgmt0"
  check "MGMT: Engine meldet weiter vollständig" sh -c "$STATUS | grep -q 'host prepared'"
  ip -n relay link del mgmt0
  reapply
  check "MGMT-Regeln verschwinden mit der Standardroute" sh -c "! ip -n relay rule show | grep -q 7355"
  kill "$LISTEN_T2" "$LISTEN_HOST" 2>/dev/null
  ip netns exec relay sh -c 'echo 0 > /proc/sys/net/ipv4/ip_forward'
  ip -n t1 route del 10.10.2.0/24
  ip -n t2 route del 10.10.1.0/24

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
    run_case "mDNS-Instanz$1: von der MGMT-Schnittstelle in beide Trunk-Netze (Relay-Adresse als Absender)" mg \
      "-src 198.19.0.100 $mdns_send from-mgmt$2" \
      "t1 -dport 5353 -src 10.10.1.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "t2 -dport 5353 -src 10.10.2.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -none -timeout 800"
    run_case "mDNS-Instanz$1: aus einem Trunk-Netz ins andere und auf die MGMT-Schnittstelle" t2 \
      "-src 10.10.2.10 $mdns_send from-guests$2" \
      "t1 -dport 5353 -src 10.10.1.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500"
    run_case "mDNS-Instanz$1: erlaubter Absender im gefilterten Netz kommt in beiden anderen an" t1 \
      "-src 10.10.1.20 $mdns_send from-printer$2" \
      "t2 -dport 5353 -src 10.10.2.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500" \
      "mg -dport 5353 -src 198.18.0.56 -dst 224.0.0.251 -count 1 -max 1 -timeout 1500"
    run_case "mDNS-Instanz$1: anderer Absender im gefilterten Netz wird verworfen" t1 \
      "-src 10.10.1.10 $mdns_send from-other$2" \
      "t2 -dport 5353 -src 10.10.2.56 -none -timeout 800" \
      "mg -dport 5353 -src 198.18.0.56 -none -timeout 800"
  }
  mdns_joined() { local i; for i in mgmt0 trunk0.1101 trunk0.1102; do ip -n mrelay maddr show dev "$i" | grep -q 'inet  *224.0.0.251' || { echo "$i has not joined 224.0.0.251"; ip -n mrelay maddr show dev "$i"; return 1; }; done; }
  mengine
  sleep 1
  check "mDNS-Instanz: Konfiguration anwenden (zwei Trunk-Netze, MGMT-Schnittstelle als Gerät)" mdns_setup
  sleep 2 # start holdoff
  check "mDNS-Instanz: Gruppe 224.0.0.251 auf allen drei Schnittstellen beigetreten" mdns_joined
  mdns_cases "" 1
  check "mDNS-Instanz: zählt die Multicast-Pakete (relayed 3, src_filter 1)" sh -c "NSRELAY_SOCKET=$MD/engine.sock \"$NS\" admin status | grep 'reasons' | grep 'relayed:3' | grep -q 'src_filter:1'"
  mdns_status() { # every interface counts what arrived on it (MGMT 1, trunk networks 2 and 1), the rule saw 3 frames
    madmin status > "$MD/status.txt"
    grep -q 'vlan 4000 MGMT (mgmt0): in 1, out 2,' "$MD/status.txt" && grep -q 'vlan 1101 Orga (trunk0.1101): in 2, out 2,' "$MD/status.txt" &&
      grep -q 'vlan 1102 Gaeste (trunk0.1102): in 1, out 2,' "$MD/status.txt" && grep -q '^rule .*: rx 3, relayed 3' "$MD/status.txt" || { cat "$MD/status.txt"; return 1; }
  }
  check "mDNS-Instanz: Status nennt den Eingang je Schnittstelle und die Regel" mdns_status
  check "mDNS-Instanz: MGMT-Schnittstelle steht nicht in der Firewall der Engine, die Trunk-Netze schon" sh -c "ip netns exec mrelay nft list set inet nsrelay_engine relay_if | grep -q trunk0.1101 && ! ip netns exec mrelay nft list table inet nsrelay_engine | grep -q mgmt0"
  check "mDNS-Instanz: Haupt-Relay daneben bleibt aktiv" sh -c "$STATUS | grep -q 'standby false'"
  kill -TERM "$(cat /tmp/nsrelay/engine-mdns.pid)"; sleep 0.5
  mengine
  sleep 2.5
  check "mDNS-Instanz nach Neustart: Gruppe wieder beigetreten" mdns_joined
  mdns_cases " nach Neustart" 2
  check "mDNS-Instanz: Host-Vorbereitung vollständig, auch mit schreibgeschütztem /proc/sys" sh -c "NSRELAY_SOCKET=$MD/engine.sock \"$NS\" admin status | grep -q 'host prepared'"
  kill -TERM "$(cat /tmp/nsrelay/engine-mdns.pid)"; rm -f /tmp/nsrelay/engine-mdns.pid; sleep 0.5

  # ---- phase 4: control plane (store, engine API, revisions, rollback, backup) ----
  export NSRELAY_DB=/tmp/nsrelay/relay.db NSRELAY_SOCKET=/tmp/nsrelay/engine.sock
  flatout() { # expectation for t2: "" (arrives) or "-none"
    run_case "$1" t1 "-src 10.10.1.10 -dst 255.255.255.255 -sport $2 -dport 23757 -payload p4"       "t2 -dport 23757 -src 10.10.1.10 $3 -timeout 1000"
  }
  check "admin init -preset lab" "$NS" admin init -preset lab -parent trunk0
  check "admin apply (Revision 1)" "$NS" admin apply -note "lab start"
  check "admin status über den Socket" sh -c "\"$NS\" admin status | grep -q 'mode active'"
  # 17. web UI: reachable on the host, never through a relay address - first the firewall stops it, without the
  # firewall the web part itself closes such connections
  ip netns exec relay "$NS" web -addr :8080 >/tmp/nsrelay/web.log 2>&1 &
  echo $! > /tmp/nsrelay/web.pid
  sleep 2
  check "Web: auf dem Host erreichbar" ip netns exec relay "$NS" health -url http://127.0.0.1:8080/healthz
  check "Web: über die Relay-Adresse nicht erreichbar (Firewall)" nodial t1 10.10.1.55:8080
  rnft delete table inet nsrelay_engine
  check "Web ohne Firewall: Verbindung kommt an, wird aber ohne Antwort geschlossen" sh -c "ip netns exec t1 \"$NS\" lab tcp-dial -addr 10.10.1.55:8080 && ! ip netns exec t1 \"$NS\" health -url http://10.10.1.55:8080/healthz"
  check "Web: Log nennt die abgewiesene Relay-Adresse" grep -q "10.10.1.55" /tmp/nsrelay/web.log
  reapply
  kill -TERM "$(cat /tmp/nsrelay/web.pid)"; rm -f /tmp/nsrelay/web.pid
  check "admin disable flatout1 flatout2" "$NS" admin disable flatout1 flatout2
  check "admin apply (Revision 2)" "$NS" admin apply -note "flatout off"
  flatout "FlatOut per CLI aus -> nicht weitergeleitet" 50010 "-none"
  check "admin enable flatout1 flatout2 + apply (Revision 3)" sh -c "\"$NS\" admin enable flatout1 flatout2 && \"$NS\" admin apply -note 'flatout on'"
  flatout "FlatOut per CLI ein -> weitergeleitet" 50011 ""
  check "admin rollback 2 (Revision 4)" "$NS" admin rollback 2
  flatout "Rollback auf Revision 2 -> FlatOut wieder aus" 50012 "-none"
  check "admin rollback 3 (Revision 5)" "$NS" admin rollback 3
  check "admin revisions zeigt 5 Stände" sh -c "\"$NS\" admin revisions | grep -c '^[0-9]' | grep -qx 5"
  # port finder (phase 8): session on table 1 only, unknown port 41234 shows up, table 2 is filtered out
  check "finder-start auf Tisch 1" "$NS" admin finder-start -vlans 1101 -seconds 30
  ip netns exec t1 "$NS" lab send -iface eth0 -src 10.10.1.10 -dst 255.255.255.255 -sport 50030 -dport 41234 -size 20 -count 3
  ip netns exec t2 "$NS" lab send -iface eth0 -src 10.10.2.10 -dst 255.255.255.255 -sport 50030 -dport 41235 -size 20 -count 3
  sleep 0.3
  check "finder zeigt Port 41234 als unbekannt" sh -c "\"$NS\" admin finder | grep -q '41234  UNKNOWN'"
  check "finder ignoriert Tisch 2" sh -c "! \"$NS\" admin finder | grep -q 41235"
  check "finder-pcap schreibt den Mitschnitt" sh -c "\"$NS\" admin finder-pcap /tmp/nsrelay/finder.pcap && [ \$(stat -c %s /tmp/nsrelay/finder.pcap) -gt 150 ]"
  check "finder-stop" "$NS" admin finder-stop
  # server probe: a Source server in gs only answers; the relay asks from 10.10.0.x and lists it with name
  ip netns exec gs "$NS" lab a2s-server -iface eth0 -ip 10.10.0.10 -name "Lab CS2" -timeout 4000 >/tmp/nsrelay/a2s.log 2>&1 &
  A2S_PID=$!
  sleep 0.3
  check "Server-Abfrage findet den CS2-Server in gs (Name, Karte, Spieler)" sh -c "\"$NS\" admin probe | grep -q '10.10.0.10:27015.*\"Lab CS2\" \"de_lab\" 3/16'"
  check "Abfrage kommt nicht als Spieler an (Relay-Adresse zählt nicht)" sh -c "! \"$NS\" admin probe | grep -q '\.1:'"
  wait "$A2S_PID" || true
  check "backup schreibt .db und .yaml" sh -c "\"$NS\" backup -dir /tmp/nsrelay/backups -keep 3 && ls /tmp/nsrelay/backups/*.db /tmp/nsrelay/backups/*.yaml"

  # engine restarts without web and database from active.json (written by the last apply)
  kill -TERM "$ENGINE_PID"; rm -f /tmp/nsrelay/engine.pid; sleep 0.5
  mv "$NSRELAY_DB" "$NSRELAY_DB.away"
  ip netns exec relay "$NS" engine -snapshot "$SNAP" -socket /tmp/nsrelay/engine.sock >/tmp/nsrelay/engine-restart.log 2>&1 &
  echo $! > /tmp/nsrelay/engine-restart.pid
  sleep 2.5
  flatout "Engine-Neustart ohne Web/DB aus active.json -> FlatOut läuft" 50013 ""
  mv "$NSRELAY_DB.away" "$NSRELAY_DB"

  # first apply while broadcasts flow (live network 2026-11-16: receivers started before the first state -> panic)
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
  check "Erstes apply unter laufendem Broadcast-Verkehr" "$NS" admin apply -note "first apply under traffic"
  for p in $SENDERS; do wait "$p"; done
  check "Engine lebt danach und ist aktiv" sh -c "\"$NS\" admin status | grep -q 'mode active'"
  check "Kein panic im Engine-Log" sh -c "! grep -q panic /tmp/nsrelay/engine-first.log"
fi

echo
echo "lab: $pass passed, $fail failed"
[ $fail = 0 ]
