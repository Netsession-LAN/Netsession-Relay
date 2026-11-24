# How it works

## Components

One static Go binary `nsrelay`, two processes (two containers from the same image):

| Process | Runs as | Does |
|---|---|---|
| `nsrelay engine` | root with `CAP_NET_RAW`, `CAP_NET_ADMIN`, `CAP_CHOWN` | creates the VLAN subinterfaces (netlink), one AF_PACKET socket per VLAN, relays packets, keeps counters and alarms, serves a small HTTP API on a Unix socket |
| `nsrelay web` | unprivileged user 10001 | web UI, JSON/SSE API, SQLite database (games, VLANs, events, revisions, audit log, accounts), compiles the configuration into a snapshot and hands it to the engine |

The engine stores every applied snapshot as `active.json` and starts from it without the web part or the
database, so a broken web container never stops relaying.

```
web (SQLite) --compile--> snapshot --Unix socket--> engine --AF_PACKET--> VLAN interfaces
```

## Data path

Each VLAN socket has a kernel BPF filter that only passes frames with a group destination MAC (broadcast or
multicast) carrying IPv4/UDP that is not fragmented, plus the relay's own beacons. For each frame, in this order:

1. Frame from the relay's own MAC or a known partner relay → drop.
2. DHCP (ports 67/68) or source `0.0.0.0` → always dropped, whatever is configured.
3. **Source subnet check:** the source IP must belong to the network of the VLAN it arrived on. If it belongs to
   *another* configured VLAN, some other relay has forwarded it → drop and raise the alarm *foreign relay*.
4. Look up the rule by destination port (broadcast) or group + port (multicast). Unknown ports are counted for the
   port finder.
5. Target VLANs from the rule's matrix (never back into the ingress VLAN).
6. Payload size check.
7. **Dedup across VLANs:** the same datagram seen on another VLAN within 200 ms is a loop → drop + alarm.
8. **Storm brake:** if the relay sends more than 5000 frames/s for 3 s, the loudest pair of rule and ingress
   VLAN is paused for 60 s. Frames of a paused pair are dropped here, before the rate limits, so a flood that is
   already paused does not use up the budgets of its network and other games from there keep working.
9. **Token buckets** per (rule, source IP, port), per ingress VLAN (frames) and per ingress VLAN (copies the
   frames fan out to).
10. Fan-out: one frame per target VLAN. Source MAC is the relay's interface; **source IP stays the original** by
    default (many games answer the sender directly); the destination becomes the target VLAN's broadcast address
    or stays `255.255.255.255`, depending on the rule.

Measured cost of the whole decision with every protection on, for a frame that is copied to three VLANs: about
0.4 µs on a current desktop CPU (Ryzen 9 7950X3D), without allocations. That is `BenchmarkProcessGuarded` in
`internal/engine`; the emit function is a no-op there, so the `sendto` system calls of the real engine are not
included. Without the protections (`BenchmarkProcess`) it is about 0.2 µs.

## Why no DSCP/TTL marking

`udpbroadcastrelay` marks forwarded packets with its instance ID in the DSCP field and ignores its own marks. Two
relays (or two instances on different boxes) do not know each other's marks and forward each other's copies forever.
Netsession Relay instead relies on the source-subnet check, dedup and rate limits — a loop cannot form even if
another relay forwards with original source addresses.

## Dual-active guard

Every engine sends a small beacon (EtherType 0x88B5) on each VLAN once per second. If an active engine sees the
beacon of another active engine that started earlier, it goes to **standby** and raises an alarm. A cloned VM
therefore never doubles the traffic. Engines also wait 1.5 s after start before sending.

## Multicast

For each multicast rule the engine joins the group on every VLAN interface (`IP_ADD_MEMBERSHIP`), so the switches'
IGMP snooping forwards the group to the relay. Receive and send still go through AF_PACKET.

## Configuration and revisions

The web part stores VLANs, groups, the matrix, events and which games are on. "Apply" compiles everything into a
snapshot (one merged port table; conflicts between games on the same port are resolved and shown as warnings),
sends it to the engine and records a revision with the full configuration. Rolling back restores a revision's
configuration and applies it as a new revision.

## Host hardening

A host with an address in every game network must not become a router between them, and its services must not be
reachable from there — yet Docker enables `net.ipv4.ip_forward`. The engine therefore shields the host itself
(`internal/hostprep`): per relay interface it switches forwarding off over netlink (which works in a container
where `/proc/sys` is read-only), and it owns an nftables table `nsrelay_engine` that drops forwarding from and to
the relay interfaces and lets only ping, IGMP and answers in. The interface with the default route is never
touched. AF_PACKET sees packets before netfilter, so the relay itself is not affected. `deploy/host/setup.sh` is
an optional, stricter variant for dedicated machines.
