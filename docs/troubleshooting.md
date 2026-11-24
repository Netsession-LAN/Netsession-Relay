# Troubleshooting

## "Players at another table don't see my game"

The UI is German by default; names are given as **German** (English). Open the game's page (**Spiele** (Games) →
the game) and read **"Wird es gefunden?"** ("Can it be found?"). It names the first link that fails:

| Shown | Meaning and fix |
|---|---|
| Game is off | Switch it on under **Spiele** (Games). |
| No packets arrive | Nobody searches/hosts yet, the port in the database is wrong, or the client's firewall blocks it. Check **Übersicht** (Overview) → "Unbekannte Ports" ("Unknown ports") while someone opens the LAN browser — a new port there is your game. |
| Packets arrive, nothing copied | Look at the drop reasons under **System**, table below. |
| Copied, still not found | The game answers or connects by unicast. Make sure the **game ports** listed on the game page are allowed between the table networks (router/ACL). |

The drop reasons you will usually meet. **System** shows the wording; `nsrelay admin status` (line `reasons`),
`/metrics` and the monitoring JSON use the key:

| Shown (German) | English | Key | Meaning |
|---|---|---|---|
| Limit erreicht | Limit reached | `rate` | more packets than the rate limit of the game, the sender or the network allows |
| Kein Ziel-Netz | No target network | `no_target` | the matrix does not connect the group of this network with any other, or the other networks are off |
| Absender passt nicht zum Netz | Sender does not match the network | `src_subnet` | the sender address is outside the subnet entered for this VLAN: the network is entered wrong, or the packet is a copy from another relay |
| Absender nicht freigegeben | Sender not allowed | `src_filter` | the game is limited to certain senders and this is not one of them |
| Port ohne Spiel | Port without a game | `unknown_port` | a broadcast on a port no switched-on game uses (see "Unknown ports") |
| Nicht gesendet (Betriebsart) | Not sent (mode) | `mode` | the relay is in observe, paused or standby |
| Kopie eines fremden Relays | Copy from a foreign relay | `foreign_relay` | another relay forwarded this packet already |
| Regel pausiert (Sturm-Bremse) | Rule paused (storm brake) | `auto_paused` | the storm brake has paused this game for this network for 60 s |

Client-side causes that look like relay problems:

- **Windows firewall profile "Public"** or rules limited to "local subnet" drop packets from other subnets.
  Set the network to "Private" or allow the game for all remote addresses.
- **Several network adapters** (VirtualBox, Hyper-V, VPN) — some games (FlatOut) put the address of the wrong
  adapter into their announcement. Disable unused adapters.
- **Steam lobby games** (`relay_needed: no`) do not use LAN broadcast at all; a relay cannot help.

## Alarms

| Shown (German) | English | Key | Meaning |
|---|---|---|---|
| Fremdes Relay aktiv | Foreign relay active | `foreign_relay` | Another device forwards the same broadcasts with original source IPs. Expected in observe mode next to an old relay; in active mode switch the other one off. |
| Schleife erkannt | Loop detected | `loop` | The same datagram arrived in two VLANs — a cabling/bridging loop or a second relay. |
| Sturm-Bremse hat eine Regel pausiert | Storm brake has paused a rule | `storm` | The relay sent more than 5000 frames/s for 3 s; the loudest rule is paused for 60 s. |
| Zweites Relay entdeckt | Second relay detected | `dual_active` | Another Netsession Relay is active and started earlier; this one stays in standby. |
| Unbekanntes Relay-Signal | Unknown relay beacon | `unknown_beacon` | A relay with another key or a forged beacon; see "Relay beacons" in [Operating](operations.md#security-settings). |
| Netz schweigt | Network silent | `vlan_silent` | No broadcast has arrived from this network for 10 minutes while the relay is active: nobody is there, or the trunk, the VLAN tag or the interface does not deliver it. |
| Engine antwortet nicht | Engine does not answer | `engine_down` | The web part has not reached the engine for 30 s: `docker compose logs engine`. |

The key is what monitoring (Zabbix, syslog, SNMP) and `nsrelay admin status` show.

## "The host is not fully shielded" on the System page

The engine could not set up everything it wants on the host (routing off on the relay interfaces, its firewall
table, routes for the management address); the page lists what and the kernel's answer. The relay itself keeps
working. Usual causes: the engine container runs without `NET_ADMIN` or not in the host network (compare with
`compose.yaml`), or the kernel has no nftables. As a fallback prepare the host by hand:
`sudo sh deploy/host/setup.sh <trunk>`.

## A command cannot write its file

In the container only `/backups` is writable. `admin export`, `admin import` and `admin finder-pcap` take `-`
for standard output or input, e.g. `docker compose exec -T web nsrelay admin export - > relay.yaml`; see "Command
line" in [Operating](operations.md#command-line).

## The browser cannot reach the UI

The UI answers on port 8080 on every address of the host **except** the relay addresses inside the game
networks. Open it through the management address. From a PC inside a game network that works as well, as long
as your router lets you reach the management address.

## The engine container restarts

`docker compose logs engine`. If `active.json` is damaged, apply again from the web part. The engine refuses
snapshots it cannot validate and keeps the previous one.
