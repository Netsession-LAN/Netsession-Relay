# Development and lab

## Build and test

Go 1.26 or newer; `go.mod` names the toolchain (`go1.27.4`), which the go command fetches by itself when yours is older. The binary is built without CGO; only the race detector needs a C compiler.

```sh
go build ./cmd/nsrelay
go test ./...                 # unit tests (pure logic, web handlers with a fake engine, SQLite store)
gofmt -l cmd internal games   # must print nothing
go mod tidy -diff             # must print nothing
go vet ./... && GOOS=windows go vet ./...     # pure-logic packages stay portable
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
CGO_ENABLED=1 go test -race ./...
```

CI (`.github/workflows/ci.yml`) runs exactly these steps and the network lab.

Layout:

| Package | |
|---|---|
| `cmd/nsrelay` | the one binary: `engine`, `web`, `admin`, `backup`, `cleanup`, `health`, `lab`, `snmp-mib`, `version` |
| `games` | `games.yaml`, the game database, embedded into the binary |
| `internal/pkt` | Ethernet/IPv4/UDP parse and build, checksums |
| `internal/portspec` | `27015-27020,2302-2470/12` port specifications |
| `internal/gamedb` | load and validate `games/games.yaml` |
| `internal/compile` | configuration → engine snapshot (merged port table, warnings) |
| `internal/engine` | per-packet decisions and protections (portable) and the Linux runtime (AF_PACKET, beacons, IGMP, port finder, server probe) |
| `internal/netif` | VLAN subinterfaces and host interfaces via netlink (creating and deleting them is tested in the lab) |
| `internal/hostprep` | what the engine does to the host: no routing on relay interfaces, nftables table, management routes |
| `internal/engineapi` | HTTP over a Unix socket between web and engine |
| `internal/store` | SQLite: migrations, configuration, revisions, audit, accounts, monitoring targets |
| `internal/control` | presets, apply, rollback, backup and restore (shared by CLI and web) |
| `internal/auth` | Argon2id password hashes, random tokens |
| `internal/web` | UI (Go templates, vanilla JS, server-sent events), JSON API, login, translations |
| `internal/uimsg` | the type for errors that are sentences for the UI |
| `internal/eventlogo` | checks an uploaded picture and re-encodes it as the event logo |
| `internal/monitor` | the monitoring report: JSON for Zabbix, Prometheus text format |
| `internal/outputs` | lifecycle of the monitoring outputs: push targets and the SNMP agent |
| `internal/notify` | queue and worker per push target |
| `internal/syslog`, `internal/otlp`, `internal/snmp` | the senders (syslog, OpenTelemetry, SNMP traps), the SNMP agent and the MIB |
| `internal/demo` | fake engine with invented data for `nsrelay web -demo` |
| `internal/lab` | packet tools behind `nsrelay lab` for the network lab |

Texts of the web UI are written in German in the templates and the Go code; `internal/web/i18n/en-*.json` maps
each German text to its English one. A test renders every page in English and fails on a text without entry;
every page a test fetches is also checked for leftovers of a broken format verb (`%!d(...)`).

## Network lab

`lab/` builds a small LAN inside network namespaces: a VLAN-filtering bridge plays the core switch, namespaces
`gs`, `t1`–`t3` are game server and tables, `relay` and `relay2` have trunk ports, `mrelay` is a second instance
with a management interface. The real engine runs inside.

```sh
bash lab/run-docker.sh        # needs Docker with --privileged (Linux, or Docker Desktop/WSL2) and Go 1.26+
```

The script first builds the Linux binary with the Go of your machine (Go 1.26 or newer; an older Go downloads a
matching toolchain itself unless `GOTOOLCHAIN=local` is set), then runs the lab in a container.

It checks, among others: FlatOut query and answer across VLANs with the original source IP, Source engine query,
DHCP never relayed, foreign relay dropped, Minecraft multicast, unknown ports, rate limit, IGMP membership, observe
mode, snapshot change under load without loss, a second engine going to standby, a second instance for other
ports next to the first, a forged relay beacon, the storm brake, the source filter, an interface recreated under
the same name, the host shielding (no routing, firewall, management routes), a network whose interface is down
(its copies are counted as `send_error`, the others still arrive), the CLI (init/apply/rollback, export and
import through standard output and input), the port finder, the server probe, an engine restart without the web
part, the first apply while broadcasts flood in, and removing the relay (`nsrelay cleanup` and the manual
commands of the documentation).

Add a lab case for every engine feature.

## Demo mode for UI work

```sh
go run ./cmd/nsrelay web -demo -addr 127.0.0.1:8080
```

Invented engine data that changes every second, a throw-away database, accounts `demo` (admin), `op`, `view`,
password `demo`. Nothing is relayed. `-demo-setup` starts with an empty database instead, so the first-run setup
shows. The demo also serves the design reference page `/dev/komponenten`.

## UI rules

The UI follows the Netsession brand: dark only, design tokens only (`internal/web/static/ns/tokens.css`; a test
rejects colour literals in `app.css`), Anton/Inter/JetBrains Mono, one primary button per view, no CDN, no inline
scripts (strict CSP), every form works without JavaScript. Accessibility target WCAG 2.2 AA.
