#!/bin/bash
# Remove the lab built by up.sh.
for ns in relay relay2 gs t1 t2 t3; do ip netns del "$ns" 2>/dev/null; done
ip link del labsw 2>/dev/null
for p in p-relay p-relay2 p-gs p-t1 p-t2 p-t3; do ip link del "$p" 2>/dev/null; done
exit 0
