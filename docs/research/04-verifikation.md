# Verifikation der Spiele-Datenbank (Stand 2026-11-16)

Auftrag: Die Einträge in `games/games.yaml` gegen Primärquellen prüfen (Quellcode, Protokoll-Specs, offizielle
Doku, getestete LAN-Relay-Listen). Die Konfiguration der alten OPNsense-Boxen gilt nur als Hinweis. Ergebnis als
Änderungsvorschlag in [`games-changes.yaml`](games-changes.yaml): 16 geänderte Einträge und 1 neuer. Die Datei
ersetzt pro `id` den kompletten Eintrag. Probeweise in eine Kopie von `games.yaml` eingemischt (Kopie im Scratchpad,
nicht im Repo), lädt sie fehlerfrei durch `gamedb.Load` und damit durch `Validate`.

Bewertung der Quellen: **Quellcode oder Protokoll-Spec = high.** Getestete Event-Liste (SDH, gamebridge) =
medium. Einzelner Guide oder Forum = low. Ports, die ich nicht belegen konnte, stehen als „unbekannt“ drin und
sind nicht geraten.

## Kurzfassung

- **Geprüft:** 45 Einträge, darunter die FlatOut-Familie, alle Spiele, die auf den Boxen liefen und `low`/`unknown`
  waren, und die Liste aus dem Auftrag. Dazu kommen die 13 Ports ohne Spiel und die nur deaktivierten Ports aus dem
  Import-Bericht.
- **Geändert:** 16. **Neu:** 1 (`xlivelessness`). **Weiter unbekannt:** FOUC-LAN-Port, ZDaemon-LAN-Port,
  GFWL-System-Link, Rocket-League-LAN, ARMA 3, Trackmania 2020 sowie die Ports 8888, 52217 und 28975/28990/29005.
- **Wichtigste Korrekturen:**
  1. **ZDaemon 15300 ist der Master-Server-Port** (`master.zdaemon.org:15300`), kein LAN-Broadcast-Port. Den
     Eintrag auf `relay_needed: unknown` setzen und keine Discovery mehr relayen.
  2. **Warsow/Warfork suchen auf 44400–44404**, nicht nur auf 44400 (`NUM_BROADCAST_PORTS 5` im Quellcode).
  3. **CS2 hat wieder einen LAN-Tab** im Community-Serverbrowser. `relay_needed: no` war falsch. Neu: `yes`,
     27015–27020, low.
  4. **DirectPlay (47624) braucht nachweislich die Original-IP.** Laut Microsoft-Spec steht im Header die Adresse
     0.0.0.0, der Host antwortet per **TCP** an die Absender-IP. `-s 1.1.1.2` auf box51#63 hat AoE, Stronghold,
     Siedler, Anno usw. also sicher kaputt gemacht.
  5. **Source-IP per Code bestätigt** (Antwort bzw. Liste über die Paket-Absenderadresse) für Quake 3/ioq3, UT99,
     Warsow/Warfork, Zandronum, Blobby Volley 2, IW4x (MW2), Minecraft Java und xlivelessness. Für all diese Spiele
     ist `-s` schädlich.
  6. **GFWL-Spiele mit xlivelessness** suchen auf **UDP 39000**, nicht auf 3074. Das ist ein neuer Eintrag.
- **FlatOut-News:** Ports für den neuen LAN-Modus von Ultimate Carnage sind weiterhin nirgends veröffentlicht.
  **Neu ist aber:** Die FOUC-exe enthält die Bugbear-Einstellungen `Network.Port` und `Network.BroadcastPort`. Das sind
  dieselben Namen wie bei FO1/FO2 (23756/23757). Das ist ein starkes Indiz, dass der neue LAN-Modus den alten
  FO2-LAN-Code wiederverwendet. Die Default-Werte sind aber nicht belegt, also einmal messen. Bei FO1, FO2, FO4 und
  Chloe Online gibt es nichts Neues.

## Tabelle der vorgeschlagenen Änderungen

| id | Feld | alt | neu | Grund (Quelle) |
|---|---|---|---|---|
| flatout_uc | notes, sources | „Ports unbekannt“ | + Port/BroadcastPort-Settings in der exe, XLLN 39000, PCGW: kein Direct-IP | Chloe-Collection-Quellcode, PCGW |
| zandronum | confidence | medium | **high** | Doomseeker-Quellcode: Port 15101, Server = Absender |
| zdaemon | relay_needed / discovery | yes / 15300 | **unknown / –** | 15300 = Master-Server (Doomseeker-Plugin) |
| ut99 | confidence | medium | **high** | UnrealScript-Quellcode UBrowserLocalLink/UdpBeacon |
| ut2004 | notes, sources | – | + LANPort 11777 / LANServerPort 10777 | UnrealScript MasterServerLink |
| quake3 | notes, sources | – | Code-Belege, IPv6-Multicast ff04::696f:7175:616b:6533 | ioquake3-Quellcode |
| warsow | ports | 44400 | **44400-44404** | qfusion-Quellcode |
| warfork | ports, confidence | 44400, medium | **44400-44404, high** | Warfork-Quellcode |
| minecraft_java | notes, sources | – | Minecraft-Wiki-Beleg; SDH/Box hat Multicast nie getroffen | minecraft.wiki, SDH-pcap-Filter |
| blobby_volley | confidence | medium | **high** | Blobby-Volley-2-Quellcode (1234, Absender-IP) |
| cs2 | relay_needed, discovery, source_ip | no, –, unknown | **yes, 27015-27020 query, original** (low) | Hosting-Guides: LAN-Tab vorhanden |
| rocket_league | notes, game_ports | „online-only“ | Local Match seit 2017, UE3, 14001 als Kandidat | Psyonix-News 2017, PC Gamer |
| codmw2 | confidence, notes | low | **medium** | IW4x-Quellcode: Broadcast 25000–35000, Antwort an Absender |
| directplay4 | notes, sources | „Antwort unicast“ | Antwort per **TCP** an Absender-IP, Spec-Beleg | MS-Spec MC-DPL4CS |
| gfwl_system_link | relay_needed / discovery | yes / 3074 | **unknown / –** | kein Beleg für 3074 als LAN-Discovery |
| **xlivelessness** (neu) | – | – | Broadcast 39000, Spiel 39001+ | XLLN-Quellcode |

Unverändert, aber geprüft: flatout1, flatout2, flatout2_chloe_online, flatout3, flatout4, arma2, arma3, tmnf,
maniaplanet, trackmania2020, minecraft_bedrock, cod1, cod2, cod4, codwaw, aoe1, aoe2, aoe2hd, warcraft3, halo_ce,
nfsu1, nfsu2, nfsmw2005, nfs_carbon_ps_uc, stronghold, seven_days_to_die, steam_client, steam_lan_browser,
source_engine.

## Einzelbefunde

### FlatOut-Familie

- **FlatOut 1:** keine Änderung. Belegt ist es weiterhin durch den Quellcode von FlatOutOnlinePlay
  (`port_game = 23756`, `port_lobby = 23757`, Patch „reply back … instead of broadcast“).
  https://github.com/gaycoderprincess/FlatOutOnlinePlay
- **FlatOut 2:** keine neuen Quellen. PCGamingWiki: LAN = true, 8 Spieler, keine Ports eingetragen. Steam/ZOOM gehen
  online über OpenSpy. Die Frage nach der Absender-IP (IP im Payload wie bei FO1?) ist weiter nur per Mitschnitt zu
  klären. Die ZOOM-Updates vom 10.11.2026 für FO1 (v1.12) und FO2 (v1.14.1) erwähnen laut Seiten-Metadaten nur
  Grafik- und Sound-Fixes und Offline-Achievements, keine Netzwerkänderung.
  https://www.zoom-platform.com/news/new-updates-2026-11-10
- **FlatOut 2 Chloe Online:** Projektseite der Autorin: nur „port 23756 forwarded … if you intend to host“. Weiter
  keine LAN-Discovery dokumentiert, keine Änderung. https://gaycoderprincess.github.io/project/fo2-custommp
- **FlatOut: Ultimate Carnage:**
  - PCGamingWiki (per API gelesen): LAN „limited“, 8 Spieler, Versus. Es ist der Standard-MP-Modus der ZOOM-Version.
    Steam fällt ohne laufenden Steam-Client oder mit `ForceLAN=1` in `zoom_flatout.ini` darauf zurück. Ports: tcp/udp
    leer, `upnp = true`, **`direct ip = false`**. Man kommt also nur über die Browser-Discovery ins Spiel, ein Relay
    ist Pflicht.
  - **Neu:** `FlatOutUCChloeCollection/customsettings.h` liest die Einstellungstabelle der exe aus (bei `0x71B518`).
    Darin stehen `{ "Network", "Port", 0x849560 }` und `{ "Network", "BroadcastPort", 0x849564 }`. FO1/FO2 nennen ihre
    Ports genauso: Spielport 23756 und Lobby-Broadcast 23757. Wahrscheinlich nutzt der LAN-Modus also den alten
    Bugbear-Code. **Die Default-Werte habe ich nirgends gefunden.** Deshalb bleibt `relay_needed: unknown`, bis
    gemessen ist. Zeigt der Mitschnitt 23757, reicht der bestehende FO2-Eintrag schon.
    https://github.com/gaycoderprincess/FlatOutUCChloeCollection/blob/HEAD/customsettings.h
  - GFWL-Builds mit xlivelessness: Discovery läuft über XLLN-Broadcast auf 39000 (siehe neuer Eintrag). Das Problem
    „Lobby sichtbar, beim Rennstart Kick“ (XLLN-Issue #30) ist ein XLLN-Problem, kein Relay-Problem.
  - portforward.com nennt für FOUC TCP 3074 und UDP 88/3074. Das sind die Xbox-Live/GFWL-Internetports, für LAN
    irrelevant.
- **FlatOut 3:** PCGamingWiki LAN „unknown“. **FlatOut 4:** PCGamingWiki LAN = false. Keine Änderung.

### Shooter mit offenem Quellcode

- **Quake III / ioquake3**: `CL_LocalServers_f` schickt `getinfo xxx` zweimal an Broadcast auf 27960–27963
  (`PORT_SERVER 27960`, `NUM_SERVER_PORTS 4`). Der Server antwortet per `SVC_Info` an `from`, der Client speichert den
  Server unter der Absenderadresse (`CL_ServerInfoPacket`/`CL_SetServerInfoByAddress`). ioq3 schickt zusätzlich an
  IPv6-Multicast `ff04::696f:7175:616b:6533`. Nur als Notiz aufgenommen, weil auf der LAN kein IPv6 läuft.
  https://github.com/ioquake/ioq3/blob/main/code/client/cl_main.c
- **OpenArena, Urban Terror, RtCW, W:ET:** id-Tech-3-Ableger, bleiben bei 27960–27963. Nicht einzeln im Code geprüft,
  Konfidenz unverändert.
- **Warsow (qfusion) / Warfork:** `NUM_BROADCAST_PORTS 5` ab `PORT_SERVER 44400`. Antworten landen unter der
  Absenderadresse, Warfork prüft zusätzlich `NET_IsLANAddress`. Bisher wurde nur 44400 relayt, ein zweiter Server
  auf 44401 war deshalb unsichtbar.
  https://github.com/Qfusion/qfusion/blob/master/source/qcommon/qcommon.h ·
  https://github.com/Warfork/warfork-qfusion/blob/master/source/client/cl_serverlist.c
- **UT99:** Der Client schickt `REPORTQUERY` an Broadcast auf 8777–8786 (Schleife `i<10`). Der Server (`UdpBeacon`,
  bindet 8777 oder den nächsten freien Port) antwortet `ut <queryport>` per Unicast. Der Client nimmt die IP aus der
  Absenderadresse und den Port aus dem Text. Beim Start schickt der Server außerdem einmal einen Broadcast an 9777, den
  braucht der Browser nicht.
  https://eatsleeput.com/undox/Uncodex-UT99-v436/Source_ubrowser/ubrowserlocallink.html ·
  https://eatsleeput.com/undox/Uncodex-UT99-v436/Source_ipdrv/udpbeacon.html
- **UT2004:** Die eigentliche LAN-Abfrage ist nativer Code. Die Defaults in `IpDrv.MasterServerLink` sind
  `LANServerPort=10777` und `LANPort=11777`. 10777 ist durch den SDH-Test belegt, die Rolle von 11777 ist offen.
  https://eatsleeput.com/undox/Uncodex-UT2004-v3369/Source_ipdrv/masterserverlink.html
- **Zandronum:** Doomseeker `zandronumbroadcast.cpp`: `BROADCAST_LISTEN_PORT = 15101`, Server werden unter
  `(sender, senderPort)` registriert.
  https://github.com/DoomseekerTeam/Doomseeker/blob/HEAD/src/plugins/zandronum/zandronumbroadcast.cpp
- **ZDaemon:** Closed Source. Das frühere Doomseeker-ZDaemon-Plugin (2013 entfernt, Commit 93ba2676) definiert
  `EP_DefaultServerPort 10666` und `EP_DefaultMaster "master.zdaemon.org:15300"`. **15300 ist also der Master-Port.**
  Der SDH-Eintrag „15300 aktiv“ hat damit keine erkennbare Grundlage. Zu 6666 und 38927 (in SDH auskommentiert) habe ich
  keine Quelle gefunden.
  https://github.com/OpenSourceLAN/service-discovery-helper/blob/master/ports
- **Call of Duty 1/2/4:** keine neuen Primärquellen, SDH-Test auf 28960–28963 bleibt. **WaW:** keine Quelle
  gefunden, bleibt low. **MW2/IW4x:** `Discovery.cpp` broadcastet `discovery <challenge>` an jeden Port von 25000 bis
  35000, das sind rund 10 000 Pakete pro Suche. Antworten werden nur von privaten Adressen angenommen und unter der
  Absenderadresse eingetragen. Das Relay sollte nur 28960–28963 durchlassen, nicht den ganzen Bereich.
  https://github.com/iw4x/iw4x-client/blob/HEAD/src/Components/Modules/Discovery.cpp
- **CS2:** Hosting-Guides beschreiben den LAN-Tab im Community-Serverbrowser („Choose LAN, find your server“). Der
  Mechanismus ist für CS2 nicht gemessen. Angenommen ist die Steam-LAN-Abfrage wie bei allen Source-Spielen.
  https://profilerr.net/how-to-host-a-cs2-server/
- **ARMA 2:** SDH-Test (jeder 12. Port 2302–2470) bleibt. **ARMA 3:** Die BI-Wiki-Seite ist per Cloudflare
  gesperrt, Guides widersprechen sich (2302–2306 bzw. 2344/2345). Der LAN-Mechanismus ist unbelegt, bleibt low.
  Messen.
- **Halo CE:** MS KB 829473 (Host 2302, Client 2303) ist weiter die beste Quelle. Den Discovery-Mechanismus fand ich
  nicht dokumentiert, keine Änderung.

### Strategie, DirectPlay, Blizzard

- **DirectPlay 4 (47624):** MS-Spec [MC-DPL4CS]: Der Client schickt `DPSP_MSG_ENUMSESSIONS` an den Broadcast auf dem
  DirectPlay-UDP-Port, „It then listens for responses on the DirectPlay TCP/IP port“. Im Beispiel-Header steht
  `SockAddr: Port = 2300, Address = 0.0.0.0`. Der Host muss also die Paket-Absender-IP nehmen und verbindet sich per
  **TCP** zu ihr. Folge: Absender umschreiben bricht die Enumeration. Außerdem muss TCP 2300–2400 auch vom Host zum
  Client routbar sein.
  https://learn.microsoft.com/en-us/openspecs/windows_protocols/mc-dpl4cs/93c87268-5f7c-4870-ad40-a05bcf13f00d ·
  https://msdn.microsoft.com/en-us/library/cc216901.aspx
  Das betrifft aoe1, aoe2, stronghold, settlers4, anno_1602, total_annihilation, gta2 und blobby_volley (BV1). Deren
  Einträge stehen schon auf `original`, nur die Begründung ist jetzt belegt.
- **AoE 2300–2400:** Das ist kein Discovery-Bereich, sondern der DirectPlay-Spielverkehr (TCP+UDP). Er wird
  geroutet, nicht relayt. Die deaktivierten „AOE-Block“-Einträge auf box52 waren 27000–27031 (siehe unten) und
  gehören nicht zu AoE.
- **Warcraft 3 / StarCraft / Diablo II:** keine neuen Quellen. SDH-Test 6111/6112 bleibt. WC3 bleibt high.
- **Blobby Volley 2:** `LANSearchState::doSearchServers` → `PingServer("255.255.255.255", BLOBBY_PORT=1234)`. Auf
  `ID_PONG` folgt ein Connect an `PlayerIDToDottedIP(packet->playerId)`, also an die Absender-IP.
  https://github.com/danielknobe/blobbyvolley2/blob/master/src/state/NetworkSearchState.cpp

### Sonstige

- **Minecraft Java:** Minecraft Wiki: „The IP Address is the same as the sender's one“, Port aus `[AD]`. **Nebenbefund:**
  SDH filtert per pcap auf `ether dst ff:ff:ff:ff:ff:ff`. Sein Eintrag „4445“ hat den Multicast also nie getroffen,
  und auch der von SDH abgeschriebene box52-Eintrag nicht.
  https://minecraft.wiki/w/Java_Edition_protocol/Server_List_Ping
- **Minecraft Bedrock:** go-nethernet bestätigt 7551: Der Request geht an den Broadcast, die Antwort und das Signaling
  laufen an die Absenderadresse (`addresses` map). Keine Änderung nötig.
- **Rocket League:** „Local Match“ (LAN, nur PC) seit dem Autumn Update 2017. Ob es nach der Umstellung auf Epic/F2P
  noch geht, ist offen. UE3, also 14001 als Kandidat (UE3-Default `LanAnnouncePort`), unbelegt.
  https://www.rocketleague.com/news/autumn-update-local-matches-director-mode/
- **Trackmania:** TMNF/TM2 bleiben beim SDH-Test. Ein Moderator im TMF-Forum bestätigt, dass der Client nur die
  Standardports ab 2350 absucht. TM2020: Der EvoEsports-Docker startet ohne Master-Login im LAN-Modus. Die Discovery
  ist nicht belegt, bleibt low. **source_ip „original“ ist bei Trackmania eine Annahme**, Nadeo ist Closed Source.
  Gerade weil box51 hier `-s` gesetzt hatte, lohnt ein Mitschnitt.
- **NFS U1/U2/MW/Carbon:** nichts Neues gefunden, unverändert.
- **7 Days to Die:** Server-Port 26900 liegt in der Steam-LAN-Suchliste 26900–26905 (SDH „Other ports Steam
  searches“). Ob der Ingame-Browser Steam oder EOS nutzt, habe ich nicht belegt. Unverändert low.
- **xlivelessness (neu):** `xlln_network_instance_base_port` = 39000, laut Doku „used for broadcasting/communicating
  available lobbies/games/matches“. Die Instanz läuft auf 39000 + ID (meist 39001). Pakete werden mit der
  `recvfrom`-Absenderadresse verarbeitet.
  https://gitlab.com/GlitchyScripts/xlivelessness

## Die Ports ohne Spiel aus dem Import-Bericht

Box52 hat erkennbar die `ports`-Datei des service-discovery-helper übernommen, inklusive der dort auskommentierten
Zeilen.

| Port | Box | Zuordnung | Empfehlung |
|---|---|---|---|
| 6666 | box52#2 (-s) | SDH-ZDaemon-Block (dort auskommentiert). Zweck unbekannt. | weglassen |
| 10666 | box52#1 (-s) | ZDaemon/Zandronum-**Spielport** (Unicast), keine Discovery (Doomseeker-Plugin) | weglassen, in der Firewall routen |
| 38927 | box52#3 (-s) | SDH-ZDaemon-Block (auskommentiert). Zweck unbekannt. | weglassen |
| 8888 | box52#43 | **unbekannt** (nur AoE DE nutzt TCP 8888 fürs Online-Backend, kein Broadcast) | weglassen, ggf. messen |
| 27005 | box51#23 | Valve: Standard-**Client**-Port von Source/GoldSrc. Steht in SDH unter „Source games“, ohne Beleg für Discovery. | weglassen |
| 27032–27035 | box53#1–3,5 | Valve: „UDP local port 27031-27036: Remote Play“. Discovery läuft nur auf 27036 (`steam_client`). | weglassen |
| 28975, 28990, 29005 | box52#46–48 | **unbekannt.** 28960 + 15/30/45, vermutlich zusätzliche CoD-Server auf versetzten Ports. Vanilla-CoD sucht aber nur 28960–28963 ab (SDH), IW4x sucht 25000–35000 ab. | weglassen, außer jemand hostet dort bewusst |
| 52217 | box52#7 | **unbekannt** | weglassen, ggf. messen |
| 4445 | box52#5 (-s) | Minecraft Java, aber als Broadcast statt Multicast 224.0.2.60 | durch `minecraft_java` (Multicast) ersetzt |

Zusätzlich zu den nur deaktivierten Ports:

- **88, 500, 3544, 4500:** Microsofts Xbox-Netzwerk-Ports (Kerberos, IKE, Teredo, IPsec NAT-T). Das ist
  Internet-Unicast, keine LAN-Discovery. Nicht relayen.
- **27000–27031:** Steam-Client-Spielverkehr („UDP remote port 27000-27250: Game traffic“) bzw. Remote Play. Keine
  Discovery. Nicht relayen.
  Quelle: https://help.steampowered.com/en/faqs/view/2EA8-4D75-DA21-31EB

## Vor Ort messen

Allgemeines Vorgehen: Host- und Client-PC in **verschiedenen** VLANs, Relay mit der neuen Konfiguration. Auf dem
Client Wireshark mit dem Capture-Filter `udp and not port 53 and not port 5353 and not port 1900` starten, das Spiel
starten und den LAN-Browser öffnen bzw. aktualisieren. Danach nach Broadcast/Multicast filtern
(`eth.dst == ff:ff:ff:ff:ff:ff || ip.dst == 224.0.0.0/4`) und den Ziel-Port notieren. Zur Absender-IP: Eine Antwort-
oder Verbindungs-IP, die auf die Relay-Box zeigt, heißt, das Spiel braucht die Original-IP. Gegenprobe: Host und
Client im selben VLAN (ohne Relay) mitschneiden und vergleichen.

| Spiel | Was messen | Kurzanleitung |
|---|---|---|
| **FlatOut: Ultimate Carnage (ZOOM / ForceLAN=1)** | Discovery-Port, Broadcast-Richtung, IP im Payload? | Host: ZOOM-Build bzw. Steam mit `ForceLAN=1` → Multiplayer → Spiel erstellen. Client: LAN-Liste öffnen. Wireshark auf dem Client: `udp.port in {23756 23757}`, sonst alles UDP-Broadcast. Ist es 23757, den FO2-Eintrag für FOUC übernehmen. Zusätzlich `options.cfg`/Savegame-Ordner nach „BroadcastPort“ durchsuchen. |
| FlatOut 2 | antwortet der Host per Broadcast, steht die IP im Payload? | Host `-lan -host`, Client LAN-Liste. Mitschnitt auf 23757 mit und ohne `-s`. Danach muss der Client per Unicast auf 23756 zur echten Host-IP gehen. |
| ZDaemon | gibt es überhaupt einen LAN-Broadcast? | Server (zserv) im VLAN A, ZDaemon-Launcher im VLAN B mit LAN-Ansicht. Mitschnitt auf allen UDP-Broadcasts. Erwartung offen (10666? 15300?). |
| Trackmania (TMNF, TM2020) | Absender-IP nötig? | Server/Host auf 2350, Client LAN-Liste. Mitschnitt `udp.port >= 2350 && udp.port <= 2360`: Geht die Antwort an die Absender-IP der Anfrage? Einmal mit `-s`, einmal ohne. |
| ARMA 3 | LAN-Discovery-Ports | Dedicated Server mit Standardports. Client: Launcher bzw. Ingame-Browser → LAN. Alle UDP-Broadcasts notieren (2302–2470/12 wie bei ARMA 2? 2303? 27015–27020?). |
| CS2 | LAN-Tab-Mechanismus | `cs2 -dedicated +sv_lan 1 +map de_dust2` im VLAN A. Client: Community-Serverbrowser → LAN. Erwartung: Broadcast an 27015–27020, Unicast-Antwort. |
| Rocket League | gibt es Local Match noch, welche Ports? | Host: Play → Local Match → Create. Client: Local Match → Join. Mitschnitt, auf 14001 und 7777 achten. |
| UT2004 | wird 11777 benutzt? | Server im VLAN A, Client LAN-Tab. Mitschnitt auf `udp.port in {10777 11777}`. |
| GFWL System Link (ohne XLLN) | echter Discovery-Port | Ein GFWL-Spiel mit echtem xlive und System-Link-Modus. Mitschnitt auf alle Broadcasts (3074?). |
| Ports 8888, 52217 | welches Programm sendet? | Auf der LAN `udp.port in {8888 52217}` mitschneiden, Absender-PC fragen bzw. auf dem PC `netstat -abno` ausführen. |
| 7 Days to Die | Steam-LAN oder eigener Mechanismus | Dedicated Server (ServerPort 26900), Client Ingame-Browser → LAN. Mitschnitt auf 26900–26905 und 27015–27020. |

## Methodik und Grenzen

- Quellcode geklont und lokal durchsucht (Scratchpad, nicht im Repo): ioquake3, qfusion, warfork-qfusion,
  Doomseeker (inkl. Historie), blobbyvolley2, iw4x-client, go-nethernet, xlivelessness, service-discovery-helper,
  netwarlan/gamebridge sowie die FlatOut-Repos von gaycoderprincess.
- PCGamingWiki über die MediaWiki-API gelesen, weil die Webseite per Cloudflare gesperrt ist. Das Bohemia-Wiki
  (ARMA), developer.valvesoftware.com und archive.org waren für den Abruf gesperrt. Dort stützt sich der Bericht auf
  Suchergebnisse bzw. bleibt bei low.
- Die ZOOM-Newsseite liefert ihren Text per JavaScript nach. Ausgewertet wurden nur die Metadaten der Seite.
