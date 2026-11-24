# Changelog

All notable changes to Netsession Relay. Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

- Builds and CI use Go 1.27 (Go 1.26 is still enough to build); staticcheck v0.8.2, govulncheck v1.9.0.
- The container registry cleans itself after each release and weekly (multi-architecture aware).
- The wiki is rebuilt from `docs/` on every change.
- A failed rollback says why (revision missing, or a game in it no longer exists).

## [1.0.0] - 2026-11-23

First public release. In use at Netsession LAN parties.

### Engine
- One process relays LAN-discovery broadcasts and multicasts for all games and VLANs (Go, AF_PACKET).
- Keeps the original sender IP by default; per-rule destination and source modes.
- Loop and storm protection: source-subnet check (detects other relays), dedup across VLANs, token buckets per
  game, sender and VLAN, storm brake, and a dual-active guard so a second relay with the same ports goes to standby.
- IGMP joins for multicast games, so IGMP snooping lets them through.
- Optional sender filter per game, e.g. to make only one printer visible over mDNS.
- Server probe: finds servers of games that search by broadcast but answer by unicast (Steam/Source, id Tech 3).
- Port finder: records unknown broadcasts of a game to add it to the database.

### Game database
- About 150 games and platforms with their discovery ports, researched from source code, protocol documentation,
  established LAN-party port lists and captures; sources per entry in `games/games.yaml`.

### Web UI
- Live overview, games on/off with per-game diagnosis, networks and groups, history with rollback, emergency stop.
- German and English, a "98" desktop theme for fun.
- Your event's logo next to the Netsession Relay mark (PNG/JPEG, checked and re-encoded).
- Local accounts (Argon2id) and single sign-on via OpenID Connect, roles viewer, operator and admin (group
  names configurable).
- Monitoring endpoint (JSON and Prometheus), Zabbix templates, SNMP agent, syslog and OpenTelemetry export.

### Operations
- Observe mode to run next to an existing relay before switching over.
- Network-namespace lab that tests the real engine with VLANs, broadcasts, multicast, rate limits, a second relay
  and restarts, in a privileged Docker container and in CI.
