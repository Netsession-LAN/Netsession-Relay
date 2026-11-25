# Changelog

All notable changes to Netsession Relay. Versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed
- The UI scripts are split along their parts into small plain scripts (`app-*.js`, `w98-*.js`, no build step,
  same behaviour): no script file has a total complexity above 40 any more (before: 272).
- Large source files split along their responsibilities (68 new files, whole declarations moved, no code
  changed): no file sums up more than 37 cyclomatic complexity any more (before: 70).

## [1.0.3] - 2026-11-25

### Changed
- No function above a cyclomatic complexity of 7 any more (before: 17), most at 5 or below. Behaviour, messages
  and output are unchanged; the packet path stays allocation-free and as fast as before (new benchmarks for the
  per-frame helpers).
- Scrutinizer measures the unit test coverage again (Cobertura) and no longer rates test files.

### Fixed
- The SNMPv3 agent test no longer times out on slow CI machines.

## [1.0.2] - 2026-11-24

### Changed
- Maintainability: the code smells reported by qlty went from 94 to 2 (no function above a cognitive complexity
  of 17; the largest files split along their responsibilities). Behaviour is unchanged: the rendered pages, JSON
  and CLI output are byte-identical, the packet path stays allocation-free.
- Scrutinizer analyses the code and measures the unit test coverage (`.scrutinizer.yml`).

## [1.0.1] - 2026-11-24

### Security
- go-jose 4.1.9 (used by the OpenID Connect login): fixes a denial of service when parsing a provider's RSA keys
  and two panics (GHSA-mgcc-cww5-fp2q, GHSA-4w3g-jfj2-cw2q, GHSA-vvm8-gf78-28g4).
- Login throttling as documented: three attempts are free, from the fourth on each one waits (2 s, 4 s … 5 min).
- `go.mod` names the toolchain (`go1.27.4`), so building with an older Go fetches the patched standard library.

### Fixed
- The backup download under **System** works with the read-only container of `compose.yaml`.
- Two backups within the same second no longer collide.
- The port finder page showed a broken placeholder for the port.
- A failed rollback says why (revision missing, or a game in it no longer exists).

### Changed
- The presets of `nsrelay admin init` start in **observe** mode, like the setup in the browser; the `lan` preset uses
  the relay address `.250`.
- `nsrelay admin export -`, `import -` and `finder-pcap -` use standard output and input, for `docker compose exec`.
- The port finder refuses VLAN IDs that are not networks of the relay.

### Added
- `nsrelay cleanup` removes everything the engine created on the host (VLAN interfaces, firewall table, routes).
- Documentation website on Read the Docs (netsession-relay.readthedocs.io); it replaces the wiki.

### Development
- Go 1.27 for CI and the image (Go 1.26 is still enough to build).
- golangci-lint (`.golangci.yml`: staticcheck, govet, errcheck, gosec, errorlint and more); every accepted gosec
  finding carries its reason in the code. govulncheck, CodeQL code scanning and qlty (`.qlty/qlty.toml`).
- Test coverage of unit tests and the network lab together (`tools/coverage.sh`); Scrutinizer analysis (`.scrutinizer.yml`).
- The container registry cleans itself after each release and weekly.

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
