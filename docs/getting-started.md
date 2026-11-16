# Getting started

This tutorial takes you from an empty Linux VM to LAN games being found across table VLANs. It takes about
30 minutes. You need admin access to your switches/hypervisor for step 1.

## What you build

```
 table 1 (VLAN 1101)   table 2 (VLAN 1102)   ...   game servers (VLAN 1100)
        \                    |                          /
         +---------- core switch (routes unicast) -----+
                             | trunk with all game VLANs
                      +--------------+
                      | relay VM     |  engine copies discovery broadcasts between VLANs
                      | eth0 = mgmt  |  web UI on the management network
                      +--------------+
```

The relay only copies **discovery** packets (a client asks "is there a server?", a host announces "here I am").
The actual game traffic goes directly between the PCs through your routers, so those ports must be allowed
between the table networks.

## 1. Network and VM

- A Linux VM (Debian 12/13 tested), 2 vCPU, 2 GB RAM, Docker 24+.
- **Management interface** with an IP you reach the web UI on.
- **Trunk interface** carrying all game VLANs **tagged**, no IP on it. On Proxmox: a VLAN-aware bridge and
  `trunks=1100;1101-1114;…` on the vNIC **with the NIC firewall off** (the relay sends with the original sender
  addresses, the Proxmox `ipfilter` would drop them).
- Pick one free host address per game VLAN for the relay (outside the DHCP pool), e.g. `.55`.

## 2. Prepare the host

```sh
git clone https://github.com/Netsession2024/Netsession-Relay.git /srv/nsrelay-src
sudo sh /srv/nsrelay-src/deploy/host/setup.sh ens19 eth0     # trunk interface, management interface
```

`setup.sh` turns off IP forwarding (the VM must never route between game VLANs), hardens Docker so it does not
switch forwarding back on, adds an nftables filter that only lets ICMP and IGMP from the game VLANs reach the VM,
and installs a nightly backup timer. It is safe to run again.

## 3. Start the relay

Use the release image or build it yourself:

```sh
cd /srv/nsrelay-src
docker build -t nsrelay:local .
NSRELAY_IMAGE=nsrelay:local NSRELAY_HTTP_ADDR=<mgmt-ip>:8080 docker compose up -d
docker compose ps        # engine and web should become "healthy"
```

The engine starts without configuration and waits.

## 4. Describe your network

The quickest way is a preset and then adjusting it. `lab` creates four VLANs (1100–1103) on the given trunk:

```sh
docker exec nsrelay-web-1 nsrelay admin init -preset lab -parent ens19
docker exec nsrelay-web-1 nsrelay admin vlans
```

For your own layout, export the configuration, edit it and import it — or add VLANs in the web UI under
**Netz**. Each VLAN has an ID, a name, a group (`tische`, `gameserver`, `wlan`, `orga`), its network and the relay IP.
The **matrix** decides which groups see each other's games; by default tables, game servers and orga see each other,
guest WiFi does not.

## 5. Create an account and look at it

```sh
docker exec nsrelay-web-1 nsrelay admin set-password admin    # prints a new password once
```

Open `http://<mgmt-ip>:8080` and log in.

## 6. Observe first

If another relay (for example OPNsense boxes) is still running, start in **observe** mode: the engine counts
everything but sends nothing.

```sh
docker exec nsrelay-web-1 nsrelay admin mode observe
docker exec nsrelay-web-1 nsrelay admin apply
```

The overview should show traffic per VLAN. A "foreign relay" alarm means the other relay is still forwarding — that
is expected in this phase and proves your trunk delivers all VLANs.

## 7. Switch over

Stop the old relay, then:

```sh
docker exec nsrelay-web-1 nsrelay admin mode active
docker exec nsrelay-web-1 nsrelay admin apply
```

Never run two active relays for the same VLANs. The dual-active guard puts a second Netsession Relay into standby,
but it cannot stop a different product.

## 8. Test with a real game

Start a FlatOut 2 (or any game from the list) LAN host at one table and open the LAN browser at another. The game
page in the UI ("Wird es gefunden?") shows each step from "packet received" to "copied into the target VLAN".
If a game is not found, see [Troubleshooting](troubleshooting.md).

## Try it without a network

`nsrelay web -demo` serves the UI with an invented engine (accounts `demo`/`op`/`view`, password `demo`).
`bash lab/run-docker.sh` runs the real engine in a network-namespace lab, see [Development](development.md).
