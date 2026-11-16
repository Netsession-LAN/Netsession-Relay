# Operating during an event

The web UI is in German, written for crew members under stress. Pages:

| Page | What you do there |
|---|---|
| **Übersicht** | Is the relay working? Mode, packets per second, alarms, every VLAN as a key cap with its rate, games with traffic, broadcasts on unknown ports. Updates live. |
| **Spiele** | Search by name or port (`/` focuses the search), switch games on and off. Takes effect immediately; running matches are not affected. |
| **Spiel** | Per game: "Wird es gefunden?" walks through the chain (game on → packets arrive → targets → copied) and names the first link that fails. Ports, notes, sources. |
| **Netz** | Switch table VLANs on/off for this event, see traffic per VLAN, the matrix (which groups see each other). |
| **Verlauf** | Every applied configuration with who/when; roll back. Audit log in plain words. |
| **System** | Operating mode, engine facts, drop reasons, backup download. |

Roles: **viewer** (see everything), **operator** (switch games, emergency stop), **admin** (networks, mode,
rollback, accounts).

## Operating modes

| Mode | Engine |
|---|---|
| `active` | relays |
| `observe` | receives and counts, sends nothing — use it next to another relay before switching over |
| `paused` | emergency stop, nothing is relayed |
| `standby` | set automatically when another active Netsession Relay that started earlier is seen |

**Emergency stop** is in the header (operator and up) and asks before it acts.

## Command line

Everything the UI does is also available on the VM:

```sh
docker exec nsrelay-web-1 nsrelay admin games -enabled
docker exec nsrelay-web-1 nsrelay admin enable flatout1 flatout2
docker exec nsrelay-web-1 nsrelay admin apply -note "FlatOut tournament"
docker exec nsrelay-web-1 nsrelay admin revisions
docker exec nsrelay-web-1 nsrelay admin rollback 7
docker exec nsrelay-engine-1 nsrelay admin status
```

## Backup and restore

A nightly timer (03:30) and every deploy write `backups/nsrelay-<time>.db` (consistent SQLite copy) plus a
readable YAML export; the last 14 are kept. Restore:

```sh
docker stop nsrelay-web-1
docker run --rm --user 10001:10001 -v nsrelay_data:/var/lib/nsrelay-web -v /srv/nsrelay/backups:/backups \
  <image> backup restore -db /var/lib/nsrelay-web/nsrelay.db /backups/nsrelay-<time>.db
docker start nsrelay-web-1
docker exec nsrelay-web-1 nsrelay admin apply -note "after restore"
```

## Migrating from OPNsense `udpbroadcastrelay`

`nsrelay import opnsense box1.xml box2.xml …` reads `config.xml` exports and reports, per port: which game it is,
entries active on several boxes (loop risk), source rewriting for games that need the original IP, missing
multicast groups, noise services and ports without a known game. It does not change anything; the game database is
the reference.
