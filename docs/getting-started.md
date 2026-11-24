# Getting started

From an empty Linux machine to LAN games being found across tables in three steps: start the containers, open the
browser, follow the setup. No configuration file, no command line.

## What you build

```
 table 1 (VLAN 11)     table 2 (VLAN 12)     ...     game servers (VLAN 10)
        \                    |                          /
         +---------- core switch (routes unicast) -----+
                             | trunk with all game VLANs
                      +--------------+
                      | relay server |  engine copies discovery broadcasts between VLANs
                      | eth0 = mgmt  |  web UI on the management network
                      +--------------+
```

The relay only copies **discovery** packets (a client asks "is there a server?", a host announces "here I am").
The actual game traffic goes directly between the PCs through your routers, so those ports must be allowed
between the table networks.

## 1. What you need

- A Linux machine or VM (Debian 12/13 tested; 2 vCPU and 2 GB RAM are plenty) with Docker and the compose plugin.
- A **management interface**: the one with the default route, where you reach the machine.
- A **trunk interface** that carries the game VLANs tagged and has no address. On a hypervisor: a VLAN-aware
  bridge, the VLANs allowed on the virtual NIC and its **firewall / MAC and IP filter off** (the relay sends with
  the original sender addresses).
- One free address per game network for the relay, the same last number everywhere (for example `.250`),
  outside the DHCP range.

## 2. Start

```sh
git clone https://github.com/Netsession-LAN/Netsession-Relay.git
cd Netsession-Relay
docker compose up -d
```

That pulls the image (or builds it from the checkout if the registry is not reachable) and starts two containers,
`engine` and `web`. Nothing has to be set.

## 3. Set up in the browser

Open `http://<server>:8080`.

1. **Account.** The first visitor creates the admin account. The page is open until that account exists, so do
   it right after the start; the log (`docker compose logs web`) says so, too.
2. **Network.** Choose the trunk interface from the list (the management interface is shown but cannot be
   chosen), enter your networks — VLAN ID, name, subnet, group — or load the example and adjust it, and choose
   the mode. **Observe** is preselected: the relay counts and shows what it would do, but sends nothing.
3. Finish. The overview shows traffic per network as soon as somebody opens a LAN browser in a game.

The games that are usual at LAN parties are switched on from the start; more under **Spiele** (Games). The UI is
German by default, the language switch is on the setup page and in the account menu (or set `NSRELAY_LANG=en`).

## 4. Go active

In observe mode a "foreign relay" alarm means another relay is still forwarding. That is expected while you
compare; it also proves the trunk delivers all VLANs. When you are ready, stop the other relay and switch to
**Aktiv** (Active) under **System**. Never run two active relays for the same networks.

Test with a real game: host a LAN game at one table and open the LAN browser at another. The game page ("Wird es
gefunden?") shows each step from "packet received" to "copied into the target network". If a game is not found,
see [Troubleshooting](troubleshooting.md).

## What the relay does to the host

The engine container uses the network of the host and may configure it (`NET_ADMIN`). Every time it applies a
configuration it

- creates one VLAN interface per network on the trunk and gives it the relay address,
- switches routing off on these interfaces, so the host never becomes a router between your game networks —
  also when Docker has switched IP forwarding on globally,
- installs a small nftables table `nsrelay_engine`: from the game networks the host answers ping and nothing
  else, so neither SSH nor the web UI is reachable from a player's PC,
- adds routes so that answers from the management address always leave through the management gateway.

It never touches the interface with the default route. The web UI listens on port 8080 on all addresses of the
host except the relay addresses. If the engine cannot do one of these things, the relay still works and the
**System** page tells what is missing.

`deploy/host/setup.sh <trunk> [mgmt]` is optional: it hardens a machine that does nothing but relay (Docker without
forwarding and iptables, a firewall that leaves only the management interface open). Do not run it on a host that
runs other containers.

## Good to know

- **Backups** are written every day into the volume `backups` (the last 14 are kept); download one any time under
  **System**. See [Operating](operations.md#backup-and-restore).
- **Single network card?** If the game VLANs arrive on the same interface as the default route, allow it
  explicitly: put `NSRELAY_ALLOWED_IFACES=eth0` into a file `.env` next to `compose.yaml` and run
  `docker compose up -d` again.
- **Bind one address only:** `NSRELAY_HTTP_ADDR=192.0.2.10:8080` in `.env`.
- **Forgot the password?** `docker compose exec web nsrelay admin set-password <name>` prints a new one.
- **Reverse proxy, single sign-on, two relays side by side:** "Security settings" in [Operating](operations.md).
- Everything the setup does can also be scripted: `docker compose exec web nsrelay admin` lists the commands
  (`init -preset lab`, `import`, `apply` …).

## Try it without a network

`nsrelay web -demo` serves the UI with an invented engine (accounts `demo`/`op`/`view`, password `demo`),
`nsrelay web -demo-setup` shows the first-run setup. `bash lab/run-docker.sh` runs the real engine in a
network-namespace lab, see [Development](development.md).
