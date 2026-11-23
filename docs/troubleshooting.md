# Troubleshooting

## "Players at another table don't see my game"

Open the game's page in the UI and read **"Wird es gefunden?"**. It names the first link that fails:

| Shown | Meaning and fix |
|---|---|
| Game is off | Switch it on under **Spiele**. |
| No packets arrive | Nobody searches/hosts yet, the port in the database is wrong, or the client's firewall blocks it. Check **Übersicht → unbekannte Ports** while someone opens the LAN browser — a new port there is your game. |
| Packets arrive, nothing copied | Look at the drop reasons under **System**: `rate` (limit), `no_target` (the matrix does not connect these groups), `src_subnet` (the VLAN's network is entered wrong). |
| Copied, still not found | The game answers or connects by unicast. Make sure the **game ports** listed on the game page are allowed between the table networks (router/ACL). |

Client-side causes that look like relay problems:

- **Windows firewall profile "Public"** or rules limited to "local subnet" drop packets from other subnets.
  Set the network to "Private" or allow the game for all remote addresses.
- **Several network adapters** (VirtualBox, Hyper-V, VPN) — some games (FlatOut) put the address of the wrong
  adapter into their announcement. Disable unused adapters.
- **Steam lobby games** (`relay_needed: no`) do not use LAN broadcast at all; a relay cannot help.

## Alarms

| Alarm | Meaning |
|---|---|
| Fremdes Relay aktiv (*foreign relay*) | Another device forwards the same broadcasts with original source IPs. Expected in observe mode next to an old relay; in active mode switch the other one off. |
| Schleife erkannt (*loop*) | The same datagram arrived in two VLANs — a cabling/bridging loop or a second relay. |
| Sturm-Bremse (*storm*) | The relay sent more than 5000 frames/s for 3 s; the loudest rule is paused for 60 s. |
| Zweites Relay entdeckt (*dual active*) | Another Netsession Relay is active and started earlier; this one stays in standby. |

## "The host is not fully shielded" on the System page

The engine could not set up everything it wants on the host (routing off on the relay interfaces, its firewall
table, routes for the management address); the page lists what and the kernel's answer. The relay itself keeps
working. Usual causes: the engine container runs without `NET_ADMIN` or not in the host network (compare with
`compose.yaml`), or the kernel has no nftables. As a fallback prepare the host by hand:
`sudo sh deploy/host/setup.sh <trunk>`.

## The browser cannot reach the UI

The UI answers on port 8080 on every address of the host **except** the relay addresses inside the game
networks. Open it through the management address. From a PC inside a game network that works as well, as long
as your router lets you reach the management address.

## The engine container restarts

`docker logs nsrelay-engine-1`. If `active.json` is damaged, apply again from the web part. The engine refuses
snapshots it cannot validate and keeps the previous one.
