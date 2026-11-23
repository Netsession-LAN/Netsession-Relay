# Development and lab

## Build and test

Go 1.26, no CGO.

```sh
go build ./cmd/nsrelay
go test ./...                 # unit tests (pure logic, web handlers with a fake engine, SQLite store)
gofmt -l cmd internal games   # must print nothing
GOOS=windows go vet ./...     # pure-logic packages stay portable
```

Layout:

| Package | |
|---|---|
| `internal/pkt` | Ethernet/IPv4/UDP parse and build, checksums |
| `internal/portspec` | `27015-27020,2302-2470/12` port specifications |
| `internal/gamedb` | load and validate `games/games.yaml` |
| `internal/compile` | configuration → engine snapshot (merged port table, warnings) |
| `internal/engine` | per-packet decisions (portable) and the Linux runtime (AF_PACKET, beacons, IGMP) |
| `internal/netif` | VLAN subinterfaces via netlink |
| `internal/engineapi` | HTTP over a Unix socket between web and engine |
| `internal/store` | SQLite: migrations, configuration, revisions, audit, accounts |
| `internal/control` | presets, apply, rollback, backup |
| `internal/web` | UI (Go templates, vanilla JS, server-sent events) |
| `internal/lab` | test tools for the lab |

## Network lab

`lab/` builds a small LAN inside network namespaces: a VLAN-filtering bridge plays the core switch, namespaces
`gs`, `t1`–`t3` are game server and tables, `relay` and `relay2` have trunk ports. The real engine runs inside.

```sh
bash lab/run-docker.sh        # needs Docker with --privileged (Linux, or Docker Desktop/WSL2)
```

It checks, among others: FlatOut query and answer across VLANs with the original source IP, Source engine query,
DHCP never relayed, foreign relay dropped, Minecraft multicast, unknown ports, rate limit, IGMP membership, observe
mode, snapshot change under load without loss, a second engine going to standby, the CLI (init/apply/rollback),
an engine restart without the web part, and the first apply while broadcasts flood in.

Add a lab case for every engine feature.

## Demo mode for UI work

```sh
go run ./cmd/nsrelay web -demo -addr 127.0.0.1:8080
```

Invented engine data that changes every second, a throw-away database, accounts `demo` (admin), `op`, `view`,
password `demo`. Nothing is relayed.

## UI rules

The UI follows the Netsession brand: dark only, design tokens only (`internal/web/static/ns/tokens.css`; a test
rejects colour literals in `app.css`), Anton/Inter/JetBrains Mono, one primary button per view, no CDN, no inline
scripts (strict CSP), every form works without JavaScript. Accessibility target WCAG 2.2 AA.
