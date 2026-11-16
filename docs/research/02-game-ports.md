# Recherche: LAN-Discovery-Ports von PC-Spielen (Stand 2026-11-16)

Maschinenlesbare Fassung: [`games.yaml`](games.yaml) (148 Einträge). Dieses Dokument erklärt die
Befunde, vor allem zu FlatOut, und was daraus für die Relay-Konfiguration folgt.

## Kurzfassung

- **148 Einträge**: 34 mit `high`, 56 mit `medium`, 58 mit `low` Konfidenz.
  26 davon haben **keine** LAN-Broadcast-Discovery (Steam-/Online-Lobby oder Direkt-IP). Für die ist ein Relay nutzlos.
- **FlatOut 1 und FlatOut 2: UDP 23757 Broadcast (Discovery), UDP 23756 Spielverkehr.** Für FlatOut 1 ist das
  durch Quellcode belegt. Der Host schickt seine Antwort **ebenfalls als Broadcast** und trägt seine eigene IP
  **im Payload** mit. Deshalb ist das Umschreiben der Absenderadresse (`-s 1.1.1.2`) für FlatOut unkritisch.
  Für FlatOut 2 ist das sehr wahrscheinlich genauso, aber noch nicht per Mitschnitt geprüft (Details unten).
- **FlatOut: Ultimate Carnage** ist ein Versionszoo. Der neue LAN-Modus (ZOOM/Steam, seit 10.11.2026) hat noch
  **keine veröffentlichten Ports**. Auf der LAN mitschneiden.
- **FlatOut 3 und 4** haben keine LAN-Discovery (Steam online). FlatOut 4 hat laut PCGamingWiki gar kein LAN.
- **Wichtigster Konfigurations-Befund:** Bei den meisten Spielen antwortet der Server **per Unicast an die
  Absender-IP** der Suchanfrage, oder der Client nimmt die Absender-IP als Host-Adresse. Das gilt für Source/Steam,
  Warcraft 3, Minecraft Java, NFS U2/MW, DirectPlay, C&C3 und weitere. Bei diesen Spielen **zerstört `-s 1.1.1.x`
  die Discovery**, denn die Antwort geht dann an die Relay-Box. Laut `00-ist-zustand.md` läuft `-s 1.1.1.2` derzeit
  u. a. auf ARMA 2, DirectPlay 47624 und 3074. Das sollte man prüfen.
  `source_ip: original` in der YAML markiert diese Spiele (99 Einträge).

## Methodik und Quellenlage

Primärquellen, nach Verlässlichkeit:

1. **Quellcode / Protokoll-Analysen:** FlatOutOnlinePlay (FlatOut 1), C&C3-LAN-Protokoll-Gist, Unreal-Engine-Doku
   (FLANSession), go-nethernet (Minecraft Bedrock).
2. **Auf echten LAN-Events getestete Portlisten:**
   - [OpenSourceLAN service-discovery-helper](https://github.com/OpenSourceLAN/service-discovery-helper)
     (`GAMES.md` + `ports`, PAX Aus 2016, DreamHack DE). Das Tool kopiert Frames **ohne** Änderung der Absender-IP.
   - [NETWAR gamebridge](https://github.com/netwarlan/gamebridge) (ebtables-Whitelist einer US-LAN-Party, leider
     ohne Spielnamen. Ich habe die Bereiche über Default-Ports zugeordnet, dann ist die Konfidenz meist `low`).
   - [vpnparty](https://github.com/tvladyslav/vpnparty) (verifizierte Spiele).
3. **Foren, Patchnotes, PCGamingWiki, Portforward-Listen:** nur `medium`/`low`.

Wo nichts Belastbares zu finden war, steht in der YAML `ports: []` bzw. `confidence: low` mit dem Hinweis
„Wireshark nötig“. Ich habe keine Ports erfunden. Zuordnungen, die ich nur über den Default-Port eines Spiels zu
einem gamebridge-Bereich erschlossen habe, sind als „inferred“ markiert.

## FlatOut im Detail

### FlatOut 1 (2004): belegt durch Quellcode

Quelle: [gaycoderprincess/FlatOutOnlinePlay](https://github.com/gaycoderprincess/FlatOutOnlinePlay). Das Plugin
macht aus dem LAN-Modus von FlatOut 1 (exe v1.1, 2 822 144 Bytes) einen Online-Modus. Dazu muss es genau die
LAN-Mechanik umbiegen, und am Quellcode sieht man deshalb, wie die LAN-Mechanik funktioniert:

| Was | Wert | Beleg im Code |
|---|---|---|
| Spielport | **UDP 23756** | `port_game = 23756 # Gameplay port` |
| Lobby-/Discovery-Port | **UDP 23757** | `port_lobby = 23757 # Lobby broadcast port` |
| Suchanfrage des Clients | Broadcast an :23757 | `sendto`-Hook: „contact specific server ip addresses instead of broadcasting through LAN“ |
| Antwort des Hosts | **ebenfalls Broadcast** | Patch `0x4955BD`: „reply back with server info to the sender's address instead of broadcast“ |
| Host-Adresse | **im Payload** (Server-Info-Struct, 106 Bytes, Offset 0x18) | `recvfrom`-Hook überschreibt sie mit der Paket-Quelle („set ip address in server info struct to public ip“) |
| 23755 | nur Master-Server des Plugins (HTTP), nicht Teil des Spiels | |

Folgen für das Relay:

- Das Relay muss 23757 in **beide Richtungen** weiterleiten: Suchanfrage Client→alle und Antwort-Broadcast
  Host→alle. Das ist mit einem normalen Relay-Eintrag für Port 23757 auf allen beteiligten VLANs automatisch der Fall.
- Die Absender-IP ist egal. Der Client verbindet sich zur **IP im Payload**, also zur echten LAN-IP des Hosts.
  Mit `-s 1.1.1.2` funktioniert es deshalb, ohne `-s` genauso.
- Dafür muss **UDP 23756 zwischen den Tisch-VLANs geroutet und erlaubt** sein. Der Client spricht den Host direkt an.
- Hat der Host mehrere aktive Netzwerkkarten (VirtualBox, Hyper-V vEthernet, VPN, WLAN+LAN), kann die falsche IP
  im Payload landen. Dann taucht das Spiel auf, aber der Beitritt scheitert. Gegenmittel: überzählige Adapter
  deaktivieren. Das ist der häufigste Tipp in den Steam-Foren.

### FlatOut 2 (2006): Retail, Steam, GOG, ZOOM

- **Discovery: UDP 23757, Broadcast an 255.255.255.255.** Der service-discovery-helper führt FlatOut 2 als
  „Works“, ebenso die NETWAR-Whitelist.
- **Spielport: UDP 23756** (portforward.com, Chloe-Online-Doku, Steam-Foren „23756 23757 öffnen“).
  Manche Foren nennen 23756–23758 TCP+UDP. Für 23758 fand ich keinen Beleg, dass es im LAN gebraucht wird.
- **Absender-IP:** Die offiziellen Patchnotes zu v1.1 dokumentieren `-private_addr=ip`: „Forces the game to report
  this private address to other players“. Das Spiel meldet seine Adresse also **im Payload**, wie FlatOut 1.
  Netzcode und Ports stammen ebenfalls von Bugbear. Sehr wahrscheinlich ist daher auch hier `-s` unkritisch.
  **Per Mitschnitt geprüft ist das noch nicht.**
- **Versionen:** Retail 1.0/1.1/1.2, Steam (AppID 2990), GOG und ZOOM unterscheiden sich im LAN-Modus nach allem,
  was auffindbar ist, nicht. Steam und ZOOM wurden 2024 auf OpenSpy als Online-Master umgestellt. Das betrifft
  nur das Internetspiel. Laut GOG-Staff funktioniert LAN, auch gemischt Linux (Wine) und Windows.
  Einzelne Berichte über „LAN unter Windows 8.1 kaputt“ blieben ungelöst.
- **Kommandozeile** (inoffiziell, aus den Patchnotes v1.1): `-lan -host[=name]`, `-lan -join=IP[:port]`,
  `-private_addr=IP`, `-public_addr=IP[:port]`, `-password=`. Das ist der **Fallback ohne Discovery**:
  `-lan -join=<Host-IP>` braucht kein Relay.
- **Bekannte Stolperfalle:** mehrere aktive NICs, siehe FlatOut 1.

**Empfehlung FlatOut 2:** Relay 23757 wieder aktivieren, und zwar auch für das Gameserver- und das WLAN-VLAN, falls
dort jemand hostet. Laut Ist-Zustand ist der Eintrag deaktiviert und nur auf Tisch 1–14 + Orga gesetzt.
`-s 1.1.1.2` kann bleiben. **23756/UDP** zwischen allen Tisch-VLANs in der Firewall erlauben. Dann einmal mit
Wireshark prüfen. Erwartung: Antwort-Broadcast des Hosts auf 23757, danach Unicast-Verkehr Client↔Host-IP auf 23756.

### FlatOut-2-Community-Projekte

- **„FlatOut 2 Chloe Online“** (FlatOut2CustomMP, Autorin: gaycoderprincess, „Chloe“): eigener Client-Server-
  Multiplayer (127 Spieler, KI). Braucht exe v1.2 (2 990 080 Bytes). Für Hosts ist nur Port 23756 dokumentiert,
  eine LAN-Broadcast-Discovery wird nicht erwähnt. Wahrscheinlich Master-Liste bzw. IP. Kein Relay-Bedarf bekannt.
- **„Chloe Collection“** gibt es für FlatOut 1 und für Ultimate Carnage (Overhaul-Mods). Die UC-Variante bringt
  Multiplayer ohne GFWL, verbunden wird per eingegebener Server-IP, also kein Broadcast.
- **„Gaypack“** existiert so nicht. Gemeint ist vermutlich das Mod-Paket bzw. der GitHub-Account
  **gaycoderprincess**. Unter diesem Namen bitte nicht suchen bzw. ansprechen.
- **„FOUCMultiplayer“** fand ich nicht. Das nächstliegende ist die FOUC Chloe Collection.
- **„FlatOut 2 1.2 patch“**: der offizielle Patch v1.2 (US/EU, 2006). Er ist Voraussetzung für Chloe Online und
  die meisten Mods. Steam und GOG liefern bereits einen 1.2-basierten Build aus.
- **OpenSpy-Fixes** (DCxDemo Flatout2.SteamOpenSpyFix, anzz1 openspy-client, ZolikaPatch) betreffen nur den
  Online-Master, nicht LAN.

### FlatOut: Ultimate Carnage (2008)

| Variante | Multiplayer | Relay? |
|---|---|---|
| Retail / GFWL, Steam-Beta-Branch `gfwl` | GFWL inkl. System Link | System-Link-Ports nicht verifiziert (3074 ist nur eine Vermutung). Mit xlivelessness: Lobbys werden gefunden, beim Rennstart fliegen alle raus. |
| Steam Hauptbranch (seit 09.11.2026) | **native Steamworks-Lobbys** (Quick Play, Custom, Voice) | nein (Steam-Lobby, Internet nötig) |
| ZOOM Platform (seit 10.11.2026), Steam ohne laufenden Steam-Client oder `ForceLAN=1` in `zoom_flatout.ini` | **neuer LAN-Modus** | **Ports unbekannt.** Auf der LAN mit Wireshark ermitteln, sehr wahrscheinlich ein Broadcast. Einmal sauber messen lohnt sich. |
| FOUC Chloe Collection (Mod, nur Steam-GFWL-exe 4 242 504 Bytes) | Direkt-IP | nein |

### FlatOut 3 und FlatOut 4

- **FlatOut 3: Chaos & Destruction:** Online über Steam (16 Spieler), LAN bei PCGamingWiki „unknown“.
  Keine Discovery-Ports bekannt.
- **FlatOut 4: Total Insanity:** PCGamingWiki: **LAN = false**, online über Steam, lokal Hotseat.
  Kein Relay möglich. Auf der LAN braucht es Internet.
- **Wreckfest** (Bugbear-Nachfolger): Steam-Serverbrowser. Dedizierte Server sind im LAN-Tab über die
  Steam-A2S-Abfrage 27015–27020 auffindbar, Spieler-Lobbys laufen über Steam.

## Weitere Befunde, die die Konfiguration betreffen

1. **Absender-IP (`-s`) ist der kritischste Punkt.** udpbroadcastrelay-redux: `-s 1.1.1.1` setzt die IP des
   ausgehenden Relay-Interfaces und als Quellport den Zielport. `-s 1.1.1.2` setzt die IP und lässt die Ports.
   Spiele mit Unicast-Antwort an den Absender (Source, WC3, Minecraft Java, NFSU2/MW, DirectPlay, ARMA, Trackmania,
   C&C3) brauchen **die originale Absender-IP**. Nur dann kommt die Antwort beim Client an. NFSU2-Relay und
   service-discovery-helper übernehmen die Original-IP aus genau diesem Grund. Ausnahmen, bei denen `-s` egal
   bzw. nötig ist: FlatOut 1/2 (IP im Payload, Antwort per Broadcast), mDNS-Consumer wie Chromecast.
2. **OpenTTD** ignoriert weitergeleitete Broadcasts aus anderen Subnetzen (SDH-Test). Besser die
   Invite-Codes/Direkt-IP neuerer Versionen nutzen.
3. **Port-Überschneidungen:** ARMA 2 (2350) überlappt Trackmania (2350–2360). Starcraft und WC3 teilen 6112,
   C&C Generals (8086–8088) liegt in der C&C3-Range. Ein Relay-Eintrag pro Port reicht, Spiele müssen nicht
   getrennt werden.
4. **Multicast:** Minecraft Java (224.0.2.60:4445), mDNS, SSDP, LLMNR und WS-Discovery. Für Spiele ist nur
   Minecraft Java wirklich relevant. LLMNR bitte **nicht** relayen (Angriffsfläche durch Poisoning).
5. **DOSBox-IPX braucht kein Relay** (IPXNET-Server per Unicast, Port 213). **IPXWrapper** dagegen nutzt UDP-Broadcast 54792.
6. **Rauschen:** Dropbox LAN Sync (17500) steht in der NETWAR-Liste. Nicht relayen.
7. **Unreal Engine 4/5 mit OnlineSubsystemNull:** generisch UDP 14001 (LanAnnouncePort). Gilt für viele
   Indie-UE-Spiele mit „LAN“-Option, sofern nicht Steam-Sessions verwendet werden. Unity-Spiele mit
   UNet/Mirror-Discovery nutzen standardmäßig 47777.

## Offene Punkte (auf der LAN messen)

- FlatOut 2: per Wireshark bestätigen, dass die IP im Payload steht und der Host per Broadcast antwortet.
- FlatOut: Ultimate Carnage, neuer LAN-Modus (ZOOM / `ForceLAN=1`): Discovery-Port ermitteln.
- Northgard, Assetto Corsa (Content-Manager-LAN-Tab), AoE3 (2005), AoM, Supreme Commander, Company of Heroes,
  RA2/YR-CnCNet-LAN-Lobby, Crysis, Far Cry: Ports unbekannt oder nur vermutet.
- gamebridge-Bereiche ohne Zuordnung: 1301–1312, 2234–2245, 4321, 5001, 7140, 24298, 25299–25310,
  29252–29263, 30719–30730, 58202–58203, 65117. Möglich: Anfrage an NETWAR.

## Übersicht (generiert aus `games.yaml`)

BC = Broadcast, MC = Multicast. `source_ip`: original = Absender-IP muss erhalten bleiben, relay = Umschreiben
unkritisch, unknown = ungeprüft.

| Spiel | Discovery-Ports | source_ip | Konfidenz |
|---|---|---|---|
| FlatOut (2004) | 23757 (BC) | relay | high |
| FlatOut 2 (2006) | 23757 (BC) | relay | high |
| FlatOut 2 Chloe Online (custom MP plugin by gaycoderprincess) | keine (Online/Direkt-IP) | unknown | low |
| FlatOut: Ultimate Carnage (2008) | keine (Online/Direkt-IP) | unknown | medium |
| FlatOut 3: Chaos & Destruction (2011) | keine (Online/Direkt-IP) | unknown | low |
| FlatOut 4: Total Insanity (2017) | keine (Online/Direkt-IP) | unknown | medium |
| Wreckfest / Wreckfest 2 | 27015–27020 (BC) | original | low |
| Steam server browser – LAN tab (generic, all Steamworks game servers) | 27015–27020 (BC); 4242, 26900–26905, 27215 (BC) | original | medium |
| Steam client – Remote Play / In-Home Streaming / LAN content transfer discovery | 27036 (BC) | original | high |
| Source engine games (generic) | 27015–27020 (BC) | original | high |
| Counter-Strike 1.6 / Half-Life 1 (GoldSrc, HLDS) | 27015–27020 (BC) | original | medium |
| Counter-Strike: Source | 27015–27020 (BC) | original | high |
| Counter-Strike: Global Offensive (legacy) | 27015–27020 (BC) | original | high |
| Counter-Strike 2 | keine (Online/Direkt-IP) | unknown | medium |
| Team Fortress 2 | 27015–27020 (BC) | original | high |
| Half-Life 2: Deathmatch | 27015–27020 (BC) | original | high |
| Garry's Mod | 27015–27020 (BC) | original | medium |
| Left 4 Dead | 27015–27020 (BC) | original | medium |
| Left 4 Dead 2 | 27015–27020 (BC) | original | medium |
| Day of Defeat: Source | 27015–27020 (BC) | original | high |
| Insurgency (2014) / Day of Infamy | 27015–27020 (BC) | original | medium |
| Killing Floor (2009) | ? (BC) | unknown | low |
| Killing Floor 2 | 27015–27020 (BC) | original | low |
| Unreal Tournament (1999) / UT GOTY / Unreal Gold | 8777–8786 (BC) | original | medium |
| Unreal Tournament 2004 / UT2003 | 10777 (BC) | original | high |
| Unreal Tournament 3 | 14001 (BC) | original | medium |
| Unreal Engine 4/5 games with OnlineSubsystemNull LAN sessions (generic) | 14001 (BC) | original | high |
| America's Army 2.x (UE2) | 1716 (BC) | unknown | low |
| SWAT 4 | 10480–10491 (BC) | unknown | low |
| Quake (NetQuake) and DarkPlaces-based ports | 26000 (BC) | original | medium |
| Xonotic | 26000 (BC) | original | medium |
| QuakeWorld (ezQuake, FTE, nQuake) | 27500 (BC) | original | low |
| Quake II (incl. Yamagi, Q2PRO, Quake II Remastered legacy servers) | 27910 (BC) | original | medium |
| Quake III Arena / Team Arena / ioquake3 | 27960–27963 (BC) | original | high |
| OpenArena | 27960–27963 (BC) | original | medium |
| Urban Terror | 27960–27963 (BC) | original | medium |
| Return to Castle Wolfenstein (MP) | 27960–27963 (BC) | original | medium |
| Wolfenstein: Enemy Territory / ET: Legacy | 27960–27963 (BC) | original | medium |
| Star Wars Jedi Knight II: Jedi Outcast (MP) | 28070–28073 (BC) | original | medium |
| Star Wars Jedi Knight: Jedi Academy (MP) | 29070–29073 (BC) | original | medium |
| Soldier of Fortune II: Double Helix | 20100–20103 (BC) | original | low |
| Medal of Honor: Allied Assault (+ Spearhead, Breakthrough) | 12203, 12300 (BC) | original | low |
| Call of Duty (2003) / United Offensive | 28960–28963 (BC) | original | medium |
| Call of Duty 2 | 28960–28963 (BC) | original | high |
| Call of Duty 4: Modern Warfare | 28960–28963 (BC) | original | high |
| Call of Duty: World at War | 28960–28963 (BC) | original | low |
| Call of Duty: Modern Warfare 2 (2009) | 28960–28963 (BC) | original | low |
| Warsow | 44400 (BC) | original | high |
| Warfork | 44400 (BC) | original | medium |
| Cube 2: Sauerbraten (also Tesseract) | 28784 (BC) | original | medium |
| Teeworlds / DDraceNetwork (DDNet) | 8303–8310 (BC) | original | medium |
| Zandronum (Doom) | 15101 (BC) | original | medium |
| ZDaemon (Doom) | 15300 (BC) | unknown | low |
| Duke Nukem 3D (EDuke32 / Megaton / 20th Anniversary) | keine (Online/Direkt-IP) | unknown | medium |
| Descent 1/2 (DXX-Rebirth / DXX-Retro) | 42424 (BC) | original | low |
| Warcraft III: Reign of Chaos / The Frozen Throne (classic, <=1.26 .. 1.31) | 6112 (BC) | original | high |
| Warcraft III: Reforged (1.32+) | keine (Online/Direkt-IP) | unknown | medium |
| StarCraft / Brood War (1.16-1.18, Remastered) | 6111–6112 (BC) | original | medium |
| Diablo II / Lord of Destruction (classic) | 6111–6112 (BC) | unknown | low |
| Warcraft II: Battle.net Edition | 6112 (BC) | original | low |
| DirectPlay 1-7 (generic, used by many 1996-2003 games) | 47624 (BC) | original | high |
| DirectPlay 8/9 (generic) | 6073 (BC) | original | medium |
| Age of Empires (1997) / Rise of Rome | 47624 (BC) | original | medium |
| Age of Empires II: The Age of Kings / The Conquerors (2000 CD) incl. UserPatch | 47624 (BC) | original | medium |
| Age of Empires II HD (2013, Steam) | keine (Online/Direkt-IP) | unknown | low |
| Age of Empires I/II/III Definitive Edition, AoE IV, AoM Retold | keine (Online/Direkt-IP) | unknown | low |
| Age of Empires III (2005, original/Complete Collection) | ? (BC) | unknown | low |
| Age of Mythology (2002) / The Titans | ? (BC) | unknown | low |
| Halo: Combat Evolved PC / Halo Custom Edition | 2302 (BC) | original | medium |
| Freelancer | 6073 (BC) | original | low |
| Rise of Nations / Rise of Legends (original) | ? (BC) | unknown | low |
| Command & Conquer: Generals / Zero Hour | 8086 (BC) | original | medium |
| Command & Conquer 3: Tiberium Wars / Kane's Wrath | 8086–8093 (BC) | original | high |
| Command & Conquer: Red Alert 3 / Uprising | 8086–8093 (BC) | original | low |
| Red Alert 2 / Yuri's Revenge / Tiberian Sun (via CnCNet client) | ? (BC) | unknown | low |
| C&C Remastered Collection | keine (Online/Direkt-IP) | unknown | low |
| GameSpy SDK LAN browse (generic, ~2000-2014 games) | ? (BC) | original | medium |
| Battlefield 1942 (+ Road to Rome, Secret Weapons, Desert Combat etc.) | 22000 (BC) | original | medium |
| Battlefield Vietnam | 22000 (BC) | original | low |
| Battlefield 2 (+ Project Reality, Special Forces) | 29900–29910 (BC) | original | low |
| Battlefield 2142 | 29900–29910 (BC) | original | low |
| Battlefield: Bad Company 2 / BF3 and newer | keine (Online/Direkt-IP) | unknown | medium |
| F.E.A.R. (MP / Combat) | 27888 (BC) | original | low |
| Painkiller (2004) | 3455 (BC) | original | low |
| Neverwinter Nights (Diamond / Enhanced Edition) | 5121 (BC) | original | low |
| Crysis (2007) / Crysis Wars / Crysis Warhead | 64087 (BC) | unknown | low |
| Far Cry (2004) | 49001 (BC) | unknown | low |
| Blur (2010) | 50001 (BC) | unknown | medium |
| TrackMania Nations Forever / United Forever (and Nations ESWC, Sunrise, Original) | 2350–2360 (BC) | original | high |
| TrackMania 2 (Canyon/Stadium/Valley/Lagoon) / ShootMania / ManiaPlanet | 2350–2360 (BC) | original | high |
| Trackmania (2020) / Trackmania Turbo | 2350–2360 (BC) | original | low |
| Need for Speed: Underground (2003) | 9999 (BC) | original | low |
| Need for Speed: Underground 2 | 9999 (BC) | original | high |
| Need for Speed: Most Wanted (2005) | 9999 (BC) | original | high |
| Need for Speed: Carbon / ProStreet / Undercover | 9999 (BC) | original | low |
| Assetto Corsa | ? (BC) | unknown | low |
| BeamNG.drive (BeamMP) | keine (Online/Direkt-IP) | unknown | medium |
| Rocket League | keine (Online/Direkt-IP) | unknown | low |
| Minecraft: Java Edition ('Open to LAN' and LAN servers) | 4445 (MC 224.0.2.60) | original | high |
| Minecraft: Bedrock Edition (Windows/console/mobile, BDS) | 19132 (BC); 19133 (MC ff02::1); 7551 (BC) | original | medium |
| Terraria / tModLoader | keine (Online/Direkt-IP) | unknown | medium |
| Stardew Valley | keine (Online/Direkt-IP) | unknown | medium |
| Valheim | keine (Online/Direkt-IP) | unknown | medium |
| Factorio | 34196 (BC) | original | medium |
| Satisfactory | keine (Online/Direkt-IP) | unknown | medium |
| 7 Days to Die | 26900–26905 (BC) | original | low |
| ARMA 2 / Operation Arrowhead / DayZ Mod | 2302, 2314, 2326, 2338, 2350, 2362, 2374, 2386, 2398, 2410, 2422, 2434, 2446, 2458, 2470 (BC) | original | high |
| ARMA 3 / DayZ Standalone / Arma Reforger | 2303, 27016 (BC) | original | low |
| Among Us (Local) | 47777 (BC) | original | low |
| Unity games with UNet NetworkDiscovery / Mirror NetworkDiscovery (generic) | 47777 (BC) | original | medium |
| Lethal Company | keine (Online/Direkt-IP) | unknown | low |
| Stick Fight: The Game | keine (Online/Direkt-IP) | unknown | medium |
| Ultimate Chicken Horse | keine (Online/Direkt-IP) | unknown | medium |
| Northgard | ? (BC) | unknown | low |
| Torchlight II | 4549 (BC) | original | high |
| Titan Quest / Anniversary Edition | 42801 (BC) | original | high |
| Chicken Invaders 5 | 23016 (BC) | original | medium |
| OpenTTD | 3979 (BC) | original | medium |
| Stronghold / Stronghold Crusader (+ HD, Extreme) | 47624 (BC) | original | medium |
| The Settlers III / IV (original, History Edition) | 47624 (BC) | original | low |
| Anno 1602 / 1503 / 1701 / 1404 / 1800 | 47624 (BC) | original | low |
| Total Annihilation | 47624 (BC) | original | medium |
| Supreme Commander / Forged Alliance | ? (BC) | unknown | low |
| Company of Heroes (original / Legacy Edition) | 6112 (BC) | unknown | low |
| Warhammer 40,000: Dawn of War (GOTY, Dark Crusade, Soulstorm) | 6112 (BC) | unknown | low |
| Warzone 2100 | keine (Online/Direkt-IP) | unknown | medium |
| 0 A.D. | keine (Online/Direkt-IP) | unknown | medium |
| Worms Armageddon / Worms World Party (Remastered) | 17010, 17012 (BC) | original | medium |
| Worms Reloaded / Clan Wars / W.M.D / Rumble / Worms 3D/4M | keine (Online/Direkt-IP) | unknown | low |
| Blobby Volley (1, Windows 2000) / Blobby Volley 2 | 47624 (BC); 1234 (BC) | original | medium |
| Armagetron Advanced | 4534 (BC) | original | low |
| Hedgewars | keine (Online/Direkt-IP) | unknown | low |
| OpenLieroX / Liero X | 23400 (BC) | original | low |
| Soldat (1.x / Soldat 1.8 Steam) | 23073 (BC) | unknown | low |
| Toribash | keine (Online/Direkt-IP) | unknown | low |
| Serious Sam Classic: The First/Second Encounter (incl. SE1 open-source ports) | 25600 (BC) | original | low |
| Grand Theft Auto 2 | 47624 (BC) | original | low |
| GTA San Andreas – Multi Theft Auto (MTA:SA) / SA-MP / open.mp | 34219 (BC) | original | low |
| IPXWrapper (IPX/SPX over UDP for old Windows games) | 54792 (BC) | original | medium |
| DOSBox / DOSBox-X / DOSBox Staging IPX (IPXNET) | keine (Online/Direkt-IP) | unknown | high |
| NetBIOS Name Service | 137 (BC) | original | high |
| NetBIOS Datagram Service / Browser | 138 (BC) | original | high |
| mDNS / Bonjour / DNS-SD | 5353 (MC 224.0.0.251); 5353 (MC ff02::fb) | relay | high |
| SSDP / UPnP discovery | 1900 (MC 239.255.255.250) | original | high |
| LLMNR (Link-Local Multicast Name Resolution) | 5355 (MC 224.0.0.252); 5355 (MC ff02::1:3) | original | high |
| WS-Discovery (Windows network discovery, printers) | 3702 (MC 239.255.255.250) | original | high |
| Games for Windows – LIVE System Link (generic) | 3074 (BC) | unknown | low |
| Dropbox LAN Sync (noise, not a game) | 17500 (BC) | relay | high |
