# UX-Review und Neuaufbau der Weboberfläche

Stand: 2026-11-16. Anlass: der Betreiber zur Phase-6-Oberfläche: „sieht nach Netsession aus, aber die Struktur ist eine
Katastrophe“ und „die UI muss sich live aktualisieren“. Dieses Dokument hält fest, was an der alten Oberfläche falsch
war, wie die neue aufgebaut ist und was geprüft wurde. Die Brand-Regeln aus `docs/02-plan.md` 6.4 gelten weiter.

Wer nutzt das: die Crew während der LAN, am Laptop und am Handy, in einer lauten, dunklen Halle, oft unter Druck.
Die Oberfläche muss vier Fragen in Sekunden beantworten:

1. Läuft das Relay? (Betriebsart, Verkehr, Alarme)
2. Wie schalte ich ein Spiel an oder aus?
3. Warum wird ein Spiel nicht gefunden?
4. Wie halte ich alles an, und wie komme ich zurück?

## 1. Befunde an der alten Oberfläche

### Nielsen-Heuristiken

| Heuristik | Befund | Folge |
|---|---|---|
| Sichtbarkeit des Systemzustands | Nichts aktualisierte sich. Zahlen waren so alt wie der Seitenaufruf, ohne dass man es sah. Die Betriebsart stand als 12-px-Label mit Punkt in der Kopfzeile, am Handy nur als farbiger Punkt. Die wichtigste Aussage („leitet weiter“ oder nicht) stand als Halbsatz im Fließtext | Man konnte nicht sehen, ob es funktioniert |
| Übereinstimmung mit der realen Welt | Rohwerte der Datenbank in der UI: `query`, `original`, `keep`, `no_target`, `user.set_password`, Zeiten als `2026-11-16T14:44:09` in UTC | Crew muss Engine-Vokabular kennen |
| Kontrolle und Freiheit | Not-Aus ab Operator, aber kein sichtbarer Rückweg; der Hinweis stand nur in einer Meldung. Bestätigung über `window.confirm` (Browser-Standardfenster, ohne Erklärung der Folgen). Ohne JavaScript löste Not-Aus ohne Rückfrage aus | Fehlbedienung unter Stress |
| Konsistenz | Sortierung der Spieleliste „FlatOut, dann an, dann Name“: Nach einem Schaltvorgang stimmte die Reihenfolge nicht mehr. Spiele ohne LAN-Modus standen mit Schein-Schalter mitten in der Liste. System mischte Betriebsart, Engine-Daten, Backup, Revisionen und Protokoll | Suchen statt Finden |
| Fehlervermeidung | Abmelden als kleines Icon direkt neben Not-Aus, am Handy beide nur als Icon | Vertippen wahrscheinlich |
| Wiedererkennen statt Erinnern | VLAN-Kacheln zeigten für jeden Tisch denselben grauen Punkt und `0/s`. Kein Unterschied zwischen „still“, „aus“ und „Alarm“ | Kacheln ohne Aussage |
| Flexibilität | 148 Zeilen, 11.000 px lang, Suche scrollte weg, kein Tastaturkürzel, keine Zähler an den Filtern | Schalten dauerte zu lang |
| Minimalismus | ID unter jedem Spielnamen, Spalte „Sicherheit“ für jede Zeile, zehn Spalten in der Netz-Tabelle auch am Handy | Rauschen |
| Fehler erkennen und beheben | Für „Spiel wird nicht gefunden“ gab es nur eine Regeltabelle mit Zählern. Die Deutung blieb der Crew überlassen | Kernaufgabe nicht unterstützt |
| Hilfe | Fehlermeldungen des Schalters verschwanden nach 9 s von selbst | Fehler übersehen |

### WCAG 2.2 AA (mit den echten Token-Farben gerechnet)

| Kriterium | Alt | Neu |
|---|---|---|
| 1.4.1 Farbe nicht allein | Betriebsart am Handy nur Punktfarbe, Schalter nur grün/grau, VLAN-Status nur Punkt | Jeder Zustand hat Icon und Wort: Chip „Aktiv“, Schalter „An/Aus“, Kachel „Verkehr/still/aus/Alarm“ |
| 1.4.3 Kontrast Text | Eis/Nacht 16,3, Nebel/Nacht 7,6, Rot/Nacht 5,1: in Ordnung. Primärknopf mit Verlauf: Weiß auf Netsession Rot nur 3,8 (15 px fett ist kein „großer Text“) | Primärknopf einfarbig Glut: 4,5; beim Überfahren Rot Dunkel: 5,9. Deaktiviertes wird nicht mehr über Transparenz gedimmt, sondern in Nebel gesetzt (7,1 auf Graphit) |
| 1.4.11 Kontrast Bedienelemente | Rahmen von Eingabefeld, Filter und ausgeschaltetem Schalter in Stahl: 1,5 auf Nacht | Eigene Kante `--edge` (Nebel zu 62 % auf Nacht gemischt, nur aus Token): 3,6 auf Nacht, 3,3 auf Graphit und Tiefblau. Schalter-Knopf: Nacht auf Erfolg 9,2, Silber auf Stahl 7,9 |
| 2.1.1 / 2.4.3 Tastatur, Reihenfolge | Bedienbar, aber kein Sprunglink; 148 Schalter vor dem Inhalt | Sprunglink „Zum Inhalt springen“, Reihenfolge Kopfzeile → Suche → Filter → Liste, `/` springt in die Suche, Esc leert sie |
| 2.4.7 / 2.4.11 Fokus | 2 px Rot, 3 px Abstand: in Ordnung | Unverändert, zusätzlich an Suchfeld und Radio-Karten sichtbar; haftende Kopfzeile verdeckt den Fokus nicht (`scroll-padding-top`) |
| 2.5.8 Zielgröße | Schalter 44 × 24, Abmelden 30 × 30, Filter 32 hoch, „Zurückrollen“ 26 hoch | Schalter, Filter, Knöpfe, Kopfzeile: mindestens 44 px hoch. Kleine Tabellenknöpfe 36 px. Kleiner sind nur Textlinks im Fließtext (erlaubt) und das Radio selbst, dessen ganze Karte klickbar ist |
| 3.3.2 Beschriftungen | Suche nur mit Platzhalter und `aria-label`, Schalter „… schalten“ | Sichtbar-verstecktes Label an der Suche, Schalter heißt wie das Spiel (`role="switch"`, `aria-checked`) |
| 4.1.3 Statusmeldungen | Ein Toast für alles, kein Live-Bereich für Alarme | Ein höflicher Live-Bereich meldet nur: neuer Alarm, Alarme beendet, Betriebsart gewechselt. Zahlen werden nicht vorgelesen. Fehler-Toast bleibt, bis man ihn schließt |
| 2.3.3 / Bewegung | Eine Schalter-Animation | `prefers-reduced-motion` schaltet alle Übergänge ab. Es blinkt und pulsiert nichts, auch ohne diese Einstellung |
| 1.4.10 Umbruch | Tabellen liefen am Handy über | Bei 390 px läuft keine Seite waagerecht über; Tabellen blenden Nebenspalten aus oder scrollen in ihrem Rahmen |

### Brand (6.4)

- Eingehalten und beibehalten: dunkel, nur Token, Red Bar einmal pro Ansicht, Anton/Inter/JetBrains Mono, Tastenkappe
  nur einzeln, keine Glas-Effekte, Icons als ein Sprite.
- Verstoß alt, behoben: zwei Primärknöpfe in der Wirkung (roter Knopf plus rot umrandeter Not-Aus mit gleichem Gewicht
  auf System). Jetzt genau ein gefüllter Knopf pro Ansicht; Not-Aus ist Outline mit roter Kante und rotem Icon.
- Info-Blau stand als Punkt für „Beobachten“ in der Kopfzeile. Bleibt Statusfarbe (Regel 10), jetzt mit Icon und Wort.
- Abweichung, bewusst: Die Kopfzeile ist unter 1024 px zweizeilig (Status und Not-Aus oben, Navigation darunter,
  waagerecht scrollbar wie im Brandbook). Einzeilig passten Navigation, Betriebsart und Not-Aus nicht nebeneinander,
  ohne die Betriebsart wieder auf einen Punkt zu kürzen.

## 2. Neuer Aufbau

### Navigation

| Punkt | Pfad | Rolle | Frage, die die Seite beantwortet |
|---|---|---|---|
| Übersicht | `/` | Betrachter | Läuft es? Wo ist Verkehr, wo ein Alarm? |
| Spiele | `/spiele`, `/spiele/<id>` | Betrachter, schalten ab Operator | Spiel an/aus. Warum wird es nicht gefunden? |
| Netz | `/netz` | Admin | Welcher Tisch ist im Relay, wer sieht wen? |
| Verlauf | `/verlauf` (neu, `/protokoll` leitet um) | Admin | Wer hat was geändert? Zurück auf einen älteren Stand |
| System | `/system` | Admin | Betriebsart wechseln, Engine-Daten, Backup |
| Not-Aus | Kopfzeile, `/notaus` | Operator | Alles anhalten |

Alle alten Pfade gelten weiter. `POST /system/rollback/<id>` leitet jetzt nach `/verlauf` zurück.

### Kopfzeile (auf jeder Seite gleich)

- **Betriebsart als Chip** mit Icon und Wort (Aktiv, Beobachten, Pausiert, Standby, Startet, Engine weg, Leer). Führt
  zur Übersicht. Aktualisiert sich live.
- **Live-Anzeige**: „Live“ mit ruhigem Punkt. Ohne JavaScript steht dort ehrlich „Stand 16:25:01“.
- **Not-Aus**: immer mit Wort, auch am Handy. Öffnet einen Dialog, der die Folgen nennt („laufende Partien bleiben,
  neue Spiele nur noch am eigenen Tisch, Admin schaltet wieder ein“). Ohne JavaScript führt derselbe Knopf auf die
  Bestätigungsseite `/notaus`. Ist das Relay schon pausiert, verschwindet der Knopf.
- **Konto** als Menü (Name, Rolle, Abmelden). Abmelden liegt nicht mehr neben Not-Aus.

### Übersicht

Reihenfolge nach Dringlichkeit, das Wichtigste zuerst und am Handy ohne Scrollen sichtbar:

1. **Zustandsfläche**: ein Satz in Anton („Relay leitet weiter“ / „Relay beobachtet nur“ / „Relay ist pausiert“ …),
   darunter die Folge in einem Satz, links Icon und Farbkante. Bei „Pausiert“ steht hier für Admins der eine
   Primärknopf „Wieder einschalten“; Operatoren lesen, dass ein Admin das tut.
2. **Vier Kennzahlen** (Brand-Muster): Spiele an (Link zur gefilterten Liste), Pakete/s rein, Kopien/s raus, Alarme.
3. **Alarme** vor den Netzen. Gruppiert nach Art, mit Handlungshinweis in normaler Sprache.
4. **Netze** als Tastenkappen-Kacheln: Name, Wort für den Zustand (Verkehr, still, aus, Alarm), Rate, darunter ein
   schmaler Balken relativ zum lautesten Netz. Ein Tisch mit Alarm trägt das Warn-Icon.
5. **Spiele mit Verkehr**: welche eingeschalteten Spiele gerade gesucht werden (Regelzähler der Engine mit Spielnamen,
   Pakete/s aus zwei Messungen). Ersetzt bis Phase 7 das geplante „Gerade gehostet“.
6. **Unbekannte Ports**.

### Spiele

- Drei Abschnitte: **FlatOut** (immer oben, rote Kante), **Alle Spiele** (alphabetisch, bleibt beim Schalten stehen),
  **Ohne LAN-Broadcast** (eingeklappt, ohne Schalter, öffnet sich bei einem Suchtreffer).
- Zeile statt Tabellenzeile: Schalter mit Wort, Name, Ports darunter, Zähler „Empfangen“ mit Punkt, der aufleuchtet,
  wenn seit der letzten Messung etwas ankam. Die ganze Namensfläche führt zum Spiel.
- Werkzeugleiste haftet am Desktop unter der Kopfzeile: Suche (Name, Alias, Port; `/` als Kürzel) und Filter
  Alle/An/Aus/Unsicher mit Zählern. `/spiele?filter=an` und `?q=` sind verlinkbar.
- Schaltet jemand anderes ein Spiel, folgt die eigene Liste ohne Neuladen.
- Betrachter sehen „An/Aus“ als Wort, keinen gesperrten Schalter.

### Spiel

- Schalter groß oben rechts.
- **„Wird es gefunden?“**: Die Seite geht die Kette durch, die ein Suchpaket nimmt, und nennt die erste Stelle, an der
  es hakt, als Satz mit Handlung: Spiel an → Regel im Relay → Relay leitet weiter → Suchanfragen kommen an → Kopien
  gehen raus. Stimmt alles, steht dort „Am Relay liegt es nicht“ mit dem Verweis auf den Spielverkehr danach.
  Aktualisiert sich, sobald sich ein Glied ändert.
- Danach Ports der LAN-Suche in Worten („Suchende fragen“, „Host meldet sich“), Zähler, Spiel-Ports, Hinweise, Quellen
  eingeklappt.

### Netz, Verlauf, System

- **Netz**: Schalter mit Rückfrage beim Ausschalten, Kappe und Name, Zähler live. Am Handy bleiben Schalter, Netz und
  Rate. Die Matrix ist eine Tabelle Gruppe × Gruppe mit Haken statt einer Liste von Pfeilen.
- **Verlauf**: Stände mit „Läuft“/„Abgelehnt“ und Zurückrollen (Dialog nennt, was passiert). Protokoll in Worten
  („Spiel eingeschaltet“, „Betriebsart geändert: Beobachten → Aktiv“), Zeiten in Ortszeit, Anmeldungen ausgeblendet
  und per Link einblendbar.
- **System**: „Läuft gerade“ neben der Auswahl, drei Karten mit Folgen, ein Knopf „Betriebsart wechseln“. Rechts die
  Engine-Daten und die verworfenen Pakete mit deutschen Gründen.

## 3. Live-Aktualisierung

- **Weg:** `GET /api/v1/events` (Server-Sent Events, Rolle Betrachter). Der Web-Teil fragt die Engine alle 2 s einmal
  für alle offenen Seiten (`liveHub`) und sendet jedem Browser dasselbe JSON, sofort beim Verbinden und dann im Takt.
- **Warum nicht den SSE-Strom der Engine durchreichen:** Die Seiten brauchen mehr als den Engine-Status: Spiele an
  (Datenbank), Spielnamen zu Regeln, gruppierte Alarme mit Text, Pakete/s je Regel. Das baut `liveNow` einmal, für
  den Seitenaufruf und für den Strom. So steht im HTML und im Strom garantiert dasselbe, und es läuft mit jeder
  Engine-Implementierung (Tests, Demo). Der Takt ist derselbe wie im Engine-Strom.
- **Was sich aktualisiert:** Betriebsart (Chip, Zustandsfläche, angebotene Aktionen, Not-Aus-Knopf), Kennzahlen,
  Netz-Kacheln und Netz-Tabelle, Alarme, Spiele mit Verkehr, unbekannte Ports, Zähler je Spiel und Regel,
  Schalterstellungen anderer Nutzer:innen, Diagnose, Engine-Daten und Verwerfungsgründe.
- **Verbindung weg:** Kommt 7 s nichts, erscheint unter der Kopfzeile ein ruhiges Band „Keine Verbindung zum
  Relay-Server. Die Zahlen sind von 16:36:05. Neuer Versuch läuft.“ Alle Live-Zahlen werden grau, die Anzeige
  wechselt auf „Getrennt“. Der Browser verbindet selbst neu (3 s), nach 20 s erzwingt das Skript einen neuen Versuch.
  Kommen wieder Daten, verschwindet das Band.
- **Sitzung abgelaufen:** Der Strom antwortet ohne Sitzung mit 401 bzw. beendet sich mit `bye`; das Band sagt
  „Melde dich neu an“.
- **Engine weg, Web erreichbar:** kein Verbindungsfehler, sondern Betriebsart „Engine weg“ in Rot mit Icon.
- **Verdeckte Tabs** geben ihre Verbindung zurück (Browser erlauben sechs pro Server) und verbinden beim Anzeigen neu.
- **Ohne JavaScript** bleibt alles bedienbar; die Seite zeigt den Stand vom Aufruf mit Uhrzeit.

## 4. Demo-Modus für die Arbeit an der Oberfläche

`nsrelay web -demo -addr 127.0.0.1:8080` startet die Oberfläche mit einer erfundenen Engine (`web.DemoEngine`) und
einer Wegwerf-Datenbank in einem temporären Ordner. Die echte Datenbank und der Engine-Socket werden nicht geöffnet.
Konten `demo` (Admin), `op` (Operator), `view` (Betrachter), Passwort `demo`. Die Oberfläche trägt „Demo“ in
Kopfzeile, Fußzeile und auf der Anmeldeseite. Die Demo-Engine übernimmt angewendete Stände (Schalten, Betriebsart,
Not-Aus wirken), erfindet Verkehr je Netz und Regel und lässt Alarme kommen und gehen. Nie für den Betrieb.

## 5. Prüfung

Geprüft am 2026-11-16 gegen den Demo-Modus in einem Linux-Container, mit Playwright und dem installierten Edge, bei
1440 × 900 und 390 × 844. Angesehen wurden Anmeldung, Übersicht (aktiv, pausiert, mit und ohne Alarm), Spiele,
Spiel (FlatOut 2), Netz, Verlauf, System, Not-Aus-Seite und -Dialog, Verbindungsverlust.

| Prüfung | Ergebnis |
|---|---|
| Konsolenfehler auf allen Seiten, beide Breiten | keine |
| Waagerechtes Überlaufen bei 390 px | keins (zwei Funde behoben: versteckte Texte in scrollenden Tabellen) |
| Zielgrößen am Handy unter 44 px | nur Textlink im Fließtext und Radio-Punkt (Karte ist das Ziel); Bildmarke 36 × 44 |
| Zielgrößen am Desktop unter 24 px | keine außer dem Radio-Punkt |
| Live: Zahlen ändern sich ohne Neuladen | ja, alle 2 s |
| Tab-Reihenfolge Spiele | Sprunglink → Marke → Navigation → Betriebsart → Not-Aus → Konto → Suche → Filter → Liste |
| `/` fokussiert die Suche, „23757“ findet 2 Spiele, Trefferzahl wird angesagt | ja |
| Schalter: Zustand, Wort, Meldung, Filterzähler | ja (der Fehlerfall ist nur im Code geprüft, nicht im Browser ausgelöst) |
| Zweite Sitzung schaltet ein Spiel | erste Seite folgt innerhalb von 2 s |
| Not-Aus: Dialog, Fokus auf „Abbrechen“, Esc schließt, Fokus zurück auf den Knopf | ja |
| Nach Not-Aus | Chip „Pausiert“, Knopf weg, „Wieder einschalten“ für Admin; offene Seite eines Operators folgt |
| Verbindungsverlust (Container angehalten) | Band nach 7 s, Zahlen grau, „Getrennt“; nach Rückkehr von selbst wieder live |
| `prefers-reduced-motion` | Übergänge 0 s |
| Ohne JavaScript | Schalten per Formular mit Meldung, Not-Aus über `/notaus`, Anzeige „Stand …“ |
| Betrachter | keine Schalter, kein Not-Aus, nur Übersicht und Spiele in der Navigation |
| Go-Tests (`go test ./...` im Container, dazu `gofmt`, `go vet` für Linux und Windows) | grün |

Neue Tests in `internal/web/live_test.go`: Ereignisstrom (Inhalt, 401 ohne Sitzung), Engine weg, Zustandstabelle,
Diagnose, neue Seiten und Rollen, Rücksprungziel nur lokal, Demo-Engine.

## 6. Offene Schwächen

- **Nicht mit Screenreader gehört.** Rollen, Namen und Live-Bereiche sind gesetzt und im Code geprüft, aber kein
  Durchgang mit NVDA oder VoiceOver.
- **Nicht am echten Gerät in der Halle geprüft**, nur im Emulator bei 390 px. Lesbarkeit im Dunkeln und mit Daumen
  sollte der Betreiber am eigenen Handy ansehen.
- **Hinweistexte der Spiele-Datenbank sind englisch** und lang. Das ist Inhalt, nicht Oberfläche; eine deutsche
  Kurzfassung pro Spiel („zweite Netzwerkkarte abschalten“) wäre für die Crew mehr wert als der Recherchetext.
- **Wer Not-Aus drückt, kann nicht selbst zurück**, wenn die Rolle Operator ist (Entscheidung Phase 6). Die Oberfläche
  sagt das jetzt vorher. Ob Operatoren wieder einschalten dürfen, sollte der Betreiber entscheiden.
- **„Wieder einschalten“ schaltet auf Aktiv**, nicht auf die Betriebsart vor dem Not-Aus (die wird nirgends gemerkt).
  Daneben steht der Link „Andere Betriebsart“.
- **Zähler je Spiel gelten je Port.** Teilen sich FlatOut 1 und 2 den Port 23757, zeigen beide dieselbe Zahl. Steht
  dabei, lässt sich ohne Host-Erkennung (Phase 7) nicht trennen.
- **„Tisch schweigt seit 12 Minuten“** kann die Kachel nicht zeigen: Die Engine liefert nur die Rate der letzten
  Sekunde. Die Kachel zeigt „still“.
- **Alarme lassen sich nicht quittieren** (Phase 7).
- **Die Matrix** scrollt am Handy in ihrem Rahmen, wenn es mehr als drei Gruppen gibt.
- **Datum ohne Jahr** in Verlauf und System („16.11. 16:35“). Für ein Werkzeug, das an einem Wochenende läuft,
  reicht das; im `datetime`-Attribut steht der volle Zeitpunkt.
