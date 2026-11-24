-- Optional source filter per game in an event (IPv4 addresses/networks, space separated, '' = no filter):
-- a prefix restricts only the VLAN whose network contains it (compile.Selection.Sources).

ALTER TABLE event_game ADD COLUMN sources TEXT NOT NULL DEFAULT '';
