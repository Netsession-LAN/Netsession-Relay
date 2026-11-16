# Netsession Relay

A UDP broadcast/multicast relay for LAN parties, with a web UI.

At a LAN party every table usually gets its own VLAN and subnet. Many games, however, find LAN servers only by
broadcast or multicast, and those packets do not cross a router. Netsession Relay sits in all game VLANs and copies
the discovery packets between them, so players at different tables see each other's LAN games.

Built by [Netsession](https://netsession.net) to replace a set of OPNsense boxes running `udpbroadcastrelay`.
Documentation inside the repository is mostly in German.

## Features

- **One process for all ports and VLANs** (Go, AF_PACKET), ~0.2 µs per packet, no per-port daemons.
- **Game database** with 149 games and their discovery ports, researched from source code, protocol docs and
  established LAN-party port lists (`games/games.yaml`, research notes in `docs/research/`). FlatOut 1/2 first.
- **Keeps the original sender IP** by default (many games answer the sender directly); per-rule destination modes.
- **Loop and storm protection without DSCP tricks:** source-subnet check (detects other relays), dedup across VLANs,
  token buckets per game/source and per VLAN, storm brake, dual-active guard (a cloned second relay goes to standby).
- **IGMP joins** for multicast games (Minecraft Java) so IGMP snooping lets them through.
- **Web UI** with live updates (server-sent events): overview, games on/off, per-game diagnosis
  ("will it be found?"), networks, history with rollback, emergency stop. Local accounts (Argon2id), roles, CSRF.
- **Observe mode** to run next to an existing relay before switching over.
- **OPNsense import report:** `nsrelay import opnsense config.xml…` compares an existing
  `os-udpbroadcastrelay` setup with the game database (duplicates, source rewriting, missing multicast groups).
- **Network-namespace lab** (`lab/`) that tests the real engine with VLANs, broadcasts, multicast, rate limits,
  a second relay and restarts — runs in a privileged Docker container and in CI.

## Quick start

Requirements: a Linux VM with one trunk interface carrying the game VLANs (untagged frames not needed) and one
management interface. Docker with host networking.

```sh
sudo sh deploy/host/setup.sh <trunk-if> <mgmt-if>   # sysctls, Docker hardening (no forwarding), nftables
docker compose up -d                                 # needs NSRELAY_IMAGE and NSRELAY_HTTP_ADDR in the environment
docker exec nsrelay-web-1 nsrelay admin init -preset lab -parent <trunk-if>   # or your own VLANs
docker exec nsrelay-web-1 nsrelay admin set-password admin
docker exec nsrelay-web-1 nsrelay admin mode observe
docker exec nsrelay-web-1 nsrelay admin apply
```

Then open `http://<mgmt-ip>:8080`. Try the UI without any network: `nsrelay web -demo`.

Build and test (Go 1.26):

```sh
go test ./...
bash lab/run-docker.sh      # network lab in a privileged container
```

## Adding a game

Add an entry to `games/games.yaml` (format in the file header), run `go test ./internal/gamedb`, and open a pull
request with your sources. Captures from real LAN sessions are very welcome.

## License

MIT for the code, see [LICENSE](LICENSE). The Netsession name and logos are not covered by the MIT license;
replace them if you run the relay for another event.
