#!/bin/bash
# Full lab run inside a privileged Linux container (used by lab/run-docker.sh and by the CI).
# Expects the linux binary at lab/.bin/nsrelay.
set -euo pipefail
cd "$(dirname "$0")"
LAB=$(pwd)
# iproute2 for the namespaces; nft and ping only to check what the engine does to the host
if ! command -v ip >/dev/null || ! command -v nft >/dev/null || ! command -v ping >/dev/null; then
  install_tools() { apt-get update -qq >/dev/null && apt-get install -y -qq iproute2 nftables iputils-ping >/dev/null 2>&1; }
  # one retry: a package mirror that hiccups must not turn the lab red
  install_tools || { echo "apt-get failed, retrying in 5 s"; sleep 5; install_tools; }
fi
NSRELAY=$LAB/.bin/nsrelay
$NSRELAY --version

bash ./up.sh
mkdir -p /tmp/nsrelay
ip netns exec relay $NSRELAY lab snapshot -out /tmp/nsrelay/active.json -parent trunk0
ip netns exec relay $NSRELAY engine -snapshot /tmp/nsrelay/active.json -socket /tmp/nsrelay/engine.sock >/tmp/nsrelay/engine.log 2>&1 &
ENGINE=$!
echo $ENGINE > /tmp/nsrelay/engine.pid
sleep 2.5 # start holdoff of the engine is 1.5 s
echo "--- interfaces in ns relay:"
ip -n relay -br addr

rc=0
SNAP=/tmp/nsrelay/active.json ENGINE_PID=$ENGINE bash ./test.sh $NSRELAY || rc=$?

# stop every engine the tests started (PID files; no pkill in minimal images)
for f in /tmp/nsrelay/*.pid; do kill -TERM "$(cat "$f")" 2>/dev/null || true; done
sleep 0.5
echo "--- engine log:"
cat /tmp/nsrelay/engine.log
if [ -f /tmp/nsrelay/engine2.log ]; then echo "--- engine 2 log:"; cat /tmp/nsrelay/engine2.log; fi
if [ -f /tmp/nsrelay/mdns/engine.log ]; then echo "--- engine log mDNS instance (trunk + management interface):"; cat /tmp/nsrelay/mdns/engine.log; fi
if [ -f /tmp/nsrelay/engine-restart.log ]; then echo "--- engine log after restart:"; cat /tmp/nsrelay/engine-restart.log; fi
if [ -f /tmp/nsrelay/web.log ]; then echo "--- web log:"; cat /tmp/nsrelay/web.log; fi
if [ -f /tmp/nsrelay/engine-first.log ]; then echo "--- engine log first apply:"; cat /tmp/nsrelay/engine-first.log; fi
bash ./down.sh
exit $rc
