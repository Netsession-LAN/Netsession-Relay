#!/usr/bin/env python3
"""One-off converter: docs/research/games.yaml (research format, 2026-11-16) -> games/games.yaml (seed format).

Kept in the repo so the conversion is reproducible. After phase 1, games/games.yaml is the source of truth
and is edited directly; do not re-run this script unless you intend to discard those edits.

Usage (from repo root):  python -I tools/convert_research_games.py
"""
import os
import sys

import yaml

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SRC = os.path.join(ROOT, "docs", "research", "games.yaml")
DST = os.path.join(ROOT, "games", "games.yaml")

NOISE = {"netbios_ns", "netbios_dgm", "mdns", "ssdp", "llmnr", "ws_discovery", "dropbox_lansync"}
PLATFORM = {
    "steam_client", "steam_lan_browser", "source_engine", "gamespy_lan_generic", "directplay4", "directplay8",
    "ipxwrapper", "unreal_engine_4_5_lan", "unity_unet_mirror", "gfwl_system_link",
}
# Decision 2026-11-16: Steam 27036 (LAN game transfer / Remote Play) off by default, switchable per event.
FORCE_OFF = {"steam_client"}
# Needs measurement on site (ZOOM LAN mode since 2026-11-10, ports unpublished).
FORCE_UNKNOWN = {"flatout_uc"}


def portspec(ports):
    """Compress a list of ints into the portspec text format used by internal/portspec."""
    ps = sorted(set(int(p) for p in ports))
    out, i = [], 0
    while i < len(ps):
        # consecutive run
        j = i
        while j + 1 < len(ps) and ps[j + 1] == ps[j] + 1:
            j += 1
        if j - i >= 1:
            out.append(f"{ps[i]}-{ps[j]}")
            i = j + 1
            continue
        # arithmetic progression with step > 1, at least 3 members
        if i + 2 < len(ps):
            step = ps[i + 1] - ps[i]
            j = i + 1
            while j + 1 < len(ps) and ps[j + 1] - ps[j] == step:
                j += 1
            if j - i >= 2:
                out.append(f"{ps[i]}-{ps[j]}/{step}")
                i = j + 1
                continue
        out.append(str(ps[i]))
        i += 1
    return ",".join(out)


def convert(e):
    gid = e["id"]
    specs = []
    for d in e.get("discovery") or []:
        if d["type"] == "none" or not d.get("ports"):
            continue
        if ":" in str(d.get("group", "")):
            continue  # IPv6 multicast: engine is IPv4-only until phase 14 (docs/02-plan.md)
        assert d["proto"] == "udp", gid
        s = {"kind": d["type"], "ports": portspec(d["ports"])}
        if d["type"] == "multicast":
            s["group"] = d["group"]
        if d.get("direction"):
            s["direction"] = d["direction"]
        specs.append(s)

    if gid in FORCE_UNKNOWN:
        relay_needed = "unknown"
    elif specs:
        relay_needed = "yes"
    elif all(d["type"] == "none" for d in e["discovery"]) and e["confidence"] != "low":
        relay_needed = "no"
    else:
        relay_needed = "unknown"

    category = "noise" if gid in NOISE else "platform" if gid in PLATFORM else "game"
    all_ports = [int(p) for s in specs for p in expand(s["ports"])]
    default_enabled = (
        relay_needed == "yes"
        and category != "noise"
        and gid not in FORCE_OFF
        and e["confidence"] in ("high", "medium")
        and all(p >= 1024 for p in all_ports)
    )

    out = {"id": gid, "name": e["name"]}
    if e.get("aliases"):
        out["aliases"] = e["aliases"]
    out.update({
        "category": category,
        "relay_needed": relay_needed,
        "default_enabled": default_enabled,
        "confidence": e["confidence"],
        "source_ip": e["source_ip"],
    })
    if specs:
        out["discovery"] = specs
    if e.get("game_ports"):
        # research format mixes {udp: [ints]} and {udp_range: "2300-2400"}; merge per protocol
        merged = {}
        for k, v in e["game_ports"].items():
            proto = k.removesuffix("_range")
            vals = list(expand(v)) if isinstance(v, str) else v
            merged.setdefault(proto, []).extend(vals)
        out["game_ports"] = {k: portspec(v) for k, v in merged.items() if v}
    out["notes"] = " ".join(str(e.get("notes", "")).split())
    if e.get("sources"):
        out["sources"] = e["sources"]
    return out


def expand(spec):
    for part in spec.split(","):
        step = 1
        if "/" in part:
            part, st = part.split("/")
            step = int(st)
        if "-" in part:
            lo, hi = (int(x) for x in part.split("-"))
            yield from range(lo, hi + 1, step)
        else:
            yield int(part)


HEADER = """\
# Netsession Relay - game database (seed). Source of truth since phase 1 (2026-11-16).
# Converted once from docs/research/games.yaml by tools/convert_research_games.py; edit this file directly now.
# Validated by `go test ./games/... ./internal/gamedb/...` (strict: unknown fields are errors).
#
# Fields
#   id               slug [a-z0-9_]+, unique
#   category         game | platform (shared stack: Steam, DirectPlay, GameSpy, ...) | noise (NetBIOS, mDNS, SSDP, ...)
#   relay_needed     yes (has discovery specs) | no (no LAN broadcast, e.g. Steam lobby only) | unknown (measure on site)
#   default_enabled  switched on in a new event by default
#   confidence       high | medium | low (see docs/research/02-game-ports.md)
#   source_ip        original = game needs the original sender IP | relay = rewriting is harmless | unknown
#   discovery[]      kind broadcast|multicast, ports (portspec: "27015-27020", "2302-2470/12", "6111,6112"),
#                    group (multicast only), direction query|announce|both,
#                    optional: dst_mode keep|subnet|limited, ttl_mode keep|decrement, rate_pps, rate_burst, payload_max
#   game_ports       unicast game traffic after discovery (NOT relayed, must be routed between VLANs), by protocol
#   notes, sources   research notes and URLs
"""


def main():
    data = yaml.safe_load(open(SRC, encoding="utf-8"))
    out = [convert(e) for e in data]
    os.makedirs(os.path.dirname(DST), exist_ok=True)
    with open(DST, "w", encoding="utf-8", newline="\n") as f:
        f.write(HEADER)
        for g in out:
            f.write("\n")
            f.write(yaml.safe_dump([g], sort_keys=False, allow_unicode=True, width=120))
    on = sum(g["default_enabled"] for g in out)
    rn = {k: sum(g["relay_needed"] == k for g in out) for k in ("yes", "no", "unknown")}
    print(f"{len(out)} games, default_enabled={on}, relay_needed={rn}", file=sys.stderr)


if __name__ == "__main__":
    main()
