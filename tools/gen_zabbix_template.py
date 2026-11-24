#!/usr/bin/env python3
"""Generate deploy/zabbix/template_nsrelay.yaml (Zabbix 7.0 export format, imports into 7.x and 8).

This is the template for the HTTP endpoint /api/v1/monitoring (one HTTP agent item, the rest depends on it). The
template for the SNMP agent, deploy/zabbix/template_nsrelay_snmp.yaml, is a different one and is generated in Go
from the MIB object list (internal/snmp/zabbix.go, `nsrelay snmp-mib -zabbix`). Both put their templates into the
same template group; link only one of them to a host.

UUIDs are derived from the item keys (uuid5), so regenerating gives the same file and Zabbix updates the
template in place on re-import. Every JSONPath used here is checked against the real report by
internal/monitor/template_test.go.

    python -I tools/gen_zabbix_template.py
"""
import pathlib
import uuid
from dataclasses import dataclass
from typing import NamedTuple, Optional

T = "NS - Netsession Relay"
GROUP = "Templates/Netsession Relay"
NS = uuid.UUID("6f1d3c52-0d1e-4c55-9a43-6e5c1a2b7d10")
OUT = pathlib.Path(__file__).resolve().parent.parent / "deploy" / "zabbix" / "template_nsrelay.yaml"
L = []
w = L.append


def u(name):
    # Zabbix wants UUIDv4 syntax: take uuid5 bytes and set the version/variant bits of v4.
    b = bytearray(uuid.uuid5(NS, name).bytes)
    b[6] = (b[6] & 0x0F) | 0x40
    b[8] = (b[8] & 0x3F) | 0x80
    return uuid.UUID(bytes=bytes(b)).hex


# The template group gets a UUID derived from its name, the same one internal/snmp/zabbix.go computes.
GROUP_UUID = u("group:" + GROUP)


def q(s):
    return "'" + s.replace("'", "''") + "'"


class Trigger(NamedTuple):
    """A trigger on one item; KEY in expr stands for the item (/template/key)."""
    expr: str
    name: str
    prio: str
    desc: str = ""


@dataclass
class Item:
    """A dependent item that takes one value out of the monitoring JSON (path is its JSONPath).

    vt is the Zabbix value type; change turns a counter into a rate per second; default is the value when the
    path is missing (older relay versions); tag is the value of the component tag.
    """
    name: str
    key: str
    path: str
    vt: str = "UNSIGNED"
    units: str = ""
    triggers: tuple = ()
    tag: str = "relay"
    change: bool = False
    default: Optional[str] = None
    desc: str = ""

    @property
    def text(self):
        return self.vt in ("CHAR", "TEXT")


@dataclass
class Proto:
    """An item prototype of the VLAN discovery: field is the entry of the VLAN in $.vlan_list."""
    name: str
    key: str
    field: str
    vt: str = "UNSIGNED"
    units: str = ""
    change: bool = False
    trig: Optional[Trigger] = None


def dep(it):
    w(f"        - uuid: {u('item:' + it.key)}")
    w(f"          name: {q(it.name)}")
    w("          type: DEPENDENT")
    w(f"          key: {q(it.key)}")
    w("          delay: '0'")
    w("          history: 30d")
    w(f"          value_type: {it.vt}")
    if it.text:
        w("          trends: '0'")
    if it.units:
        w(f"          units: {q(it.units)}")
    if it.desc:
        w(f"          description: {q(it.desc)}")
    dep_preprocessing(it)
    w("          master_item:")
    w("            key: nsrelay.monitoring")
    w("          tags:")
    w("            - tag: component")
    w(f"              value: {it.tag}")
    dep_triggers(it)


def dep_preprocessing(it):
    """The preprocessing steps of a dependent item: JSONPath, then rate or discarding unchanged text."""
    w("          preprocessing:")
    w("            - type: JSONPATH")
    w("              parameters:")
    w(f"                - {q(it.path)}")
    if it.default is not None:
        w("              error_handler: CUSTOM_VALUE")
        w(f"              error_handler_params: {q(it.default)}")
    if it.change:
        w("            - type: CHANGE_PER_SECOND")
        w("              parameters:")
        w("                - ''")
    if it.text:
        w("            - type: DISCARD_UNCHANGED_HEARTBEAT")
        w("              parameters:")
        w("                - 1h")


def dep_triggers(it):
    if not it.triggers:
        return
    w("          triggers:")
    for t in it.triggers:
        w(f"            - uuid: {u('trigger:' + it.key)}")
        w(f"              expression: {q(t.expr.replace('KEY', f'/{T}/{it.key}'))}")
        w(f"              name: {q(t.name)}")
        w(f"              priority: {t.prio}")
        if t.desc:
            w(f"              description: {q(t.desc)}")


def proto(p):
    w(f"            - uuid: {u('proto:' + p.key)}")
    w(f"              name: {q(p.name)}")
    w("              type: DEPENDENT")
    w(f"              key: {q(p.key)}")
    w("              delay: '0'")
    w("              history: 30d")
    w(f"              value_type: {p.vt}")
    if p.units:
        w(f"              units: {q(p.units)}")
    w("              preprocessing:")
    w("                - type: JSONPATH")
    w("                  parameters:")
    w(f"                    - {q('$.vlan_list[?(@.vid=={#VID})].' + p.field + '.first()')}")
    if p.change:
        w("                - type: CHANGE_PER_SECOND")
        w("                  parameters:")
        w("                    - ''")
    w("              master_item:")
    w("                key: nsrelay.monitoring")
    w("              tags:")
    w("                - tag: vlan")
    w("                  value: '{#VID} {#NAME}'")
    if p.trig:
        w("              trigger_prototypes:")
        w(f"                - uuid: {u('trigproto:' + p.key)}")
        w(f"                  expression: {q(p.trig.expr.replace('KEY', f'/{T}/{p.key}'))}")
        w(f"                  name: {q(p.trig.name)}")
        w(f"                  priority: {p.trig.prio}")
        w(f"                  description: {q(p.trig.desc)}")


def template_head():
    """The template group, the template itself and the HTTP agent item every other item depends on."""
    w("zabbix_export:")
    w("  version: '7.0'")
    w("  template_groups:")
    w(f"    - uuid: {GROUP_UUID}")
    w(f"      name: {GROUP}")
    w("  templates:")
    w(f"    - uuid: {u('template')}")
    w(f"      template: {q(T)}")
    w(f"      name: {q(T)}")
    w("      description: |")
    w("        Netsession Relay (UDP broadcast relay for LAN games).")
    w("        Polls {$NSRELAY.URL}/api/v1/monitoring every 30 s (HTTP agent). The token is created on the")
    w("        System page of the relay. Set {$NSRELAY.URL} and {$NSRELAY.TOKEN} on the host.")
    w("        Generated by tools/gen_zabbix_template.py, do not edit by hand.")
    w("      groups:")
    w(f"        - name: {GROUP}")
    w("      items:")
    w(f"        - uuid: {u('item:nsrelay.monitoring')}")
    w("          name: 'Relay: monitoring JSON'")
    w("          type: HTTP_AGENT")
    w("          key: nsrelay.monitoring")
    w("          delay: 30s")
    w("          history: 1h")
    w("          value_type: TEXT")
    w("          trends: '0'")
    w("          url: '{$NSRELAY.URL}/api/v1/monitoring'")
    w("          headers:")
    w("            - name: Authorization")
    w("              value: 'Bearer {$NSRELAY.TOKEN}'")
    w("          timeout: 5s")
    w("          tags:")
    w("            - tag: component")
    w("              value: raw")
    w("          triggers:")
    w(f"            - uuid: {u('trigger:nsrelay.monitoring')}")
    w(f"              expression: 'nodata(/{T}/nsrelay.monitoring,3m)=1'")
    w("              name: 'Relay: Web-Teil antwortet nicht'")
    w("              priority: HIGH")
    w("              description: 'Seit 3 Minuten keine Antwort von /api/v1/monitoring: Web-Container, Token oder VM prüfen.'")


def relay_items():
    """The items about the relay as a whole: engine, mode, counters, monitoring targets and drop rates."""
    dep(Item("Relay: Engine erreichbar", "nsrelay.engine.up", "$.engine_up",
             triggers=[Trigger("last(KEY)=0", "Relay: Engine antwortet nicht", "HIGH",
                               "Die Web-Rolle erreicht die Engine nicht über den Unix-Socket. Engine-Container prüfen.")]))
    dep(Item("Relay: Betriebsart", "nsrelay.mode", "$.mode", vt="CHAR", desc="active, observe, paused oder leer vor dem ersten Stand."))
    dep(Item("Relay: Standby (zweites Relay)", "nsrelay.standby", "$.standby",
             triggers=[Trigger("last(KEY)=1", "Relay: im Standby, ein anderes Relay ist aktiv", "AVERAGE",
                               "Der Wächter hat ein zweites aktives Relay gesehen und dieses angehalten.")]))
    dep(Item("Relay: leitet weiter", "nsrelay.sending", "$.sending"))
    dep(Item("Relay: Event aktiv", "nsrelay.event.active", "$.event_active"))
    dep(Item("Relay: Engine-Version", "nsrelay.engine.version", "$.engine_version", vt="CHAR"))
    dep(Item("Relay: Engine-Laufzeit", "nsrelay.uptime", "$.uptime_s", units="uptime"))
    dep(Item("Relay: Stand (Hash)", "nsrelay.snapshot", "$.snapshot", vt="CHAR"))
    dep(Item("Relay: Netze", "nsrelay.vlans", "$.vlans"))
    dep(Item("Relay: Regeln", "nsrelay.rules", "$.rules"))
    dep(Item("Relay: empfangen pro Sekunde", "nsrelay.rx.rate", "$.rx_per_s", units="fps"))
    dep(Item("Relay: gesendet pro Sekunde", "nsrelay.sent.rate", "$.sent_per_s", units="fps"))
    dep(Item("Relay: offene Alarme", "nsrelay.alarms.active", "$.alarms_active"))
    dep(Item("Relay: Selbsttest-Befunde", "nsrelay.selftest", "$.self_test_findings",
             triggers=[Trigger("last(KEY)>0", "Relay: Selbsttest meldet Befunde am Host", "INFO",
                               "Details auf der System-Seite (z. B. ip_forward, rp_filter).")]))
    dep(Item("Relay: Monitoring-Ziele", "nsrelay.outputs.total", "$.outputs_total", default="0"))
    dep(Item("Relay: Monitoring-Ziele mit Fehler", "nsrelay.outputs.failing", "$.outputs_failing", default="0",
             triggers=[Trigger("last(KEY)>0", "Relay: Monitoring-Ziel nicht erreichbar", "WARNING",
                               "Ein Syslog-, OpenTelemetry- oder SNMP-Ziel nimmt seit 2 Minuten nichts an. Details: System, Monitoring.")]))
    dep(Item("Relay: verworfene Meldungen", "nsrelay.outputs.dropped", "$.outputs_dropped", default="0"))
    for r in ["loop", "rate", "foreign_relay", "src_subnet", "auto_paused", "unknown_port", "send_error", "panic"]:
        dep(Item(f"Relay: verworfen {r} pro Sekunde", f"nsrelay.drops.rate[{r}]", f"$.drops.{r}", vt="FLOAT", units="fps",
                 change=True, default="0", tag="drops"))


# Trigger name, priority and description per alarm kind of the engine.
ALARMS = {
    "foreign_relay": ("Relay: fremdes Relay aktiv ({ITEM.LASTVALUE2})", "HIGH",
                      "Ein anderes Gerät leitet dieselben Broadcasts weiter (z. B. ein zweites Relay oder ein Router mit Broadcast-Weiterleitung). Im Modus Beobachten erwartet."),
    "loop": ("Relay: Schleife erkannt ({ITEM.LASTVALUE2})", "HIGH",
             "Dasselbe Paket kam auf mehreren VLANs an. Zweites Relay oder Brücke zwischen Netzen suchen."),
    "storm": ("Relay: Sturm-Bremse hat eine Regel pausiert ({ITEM.LASTVALUE2})", "AVERAGE",
              "Über 5000 Pakete/s, die lauteste Regel pausiert 60 s für das Netz, aus dem die Pakete kommen."),
    "dual_active": ("Relay: zweites aktives Relay entdeckt", "HIGH", "Zwei Relays für dieselben Netze. Eins muss aus."),
    "vlan_silent": ("Relay: Netz ohne Broadcasts seit 10 Minuten ({ITEM.LASTVALUE2})", "WARNING",
                    "Während eines aktiven Events kam auf einem Netz nichts an. Trunk, VLAN-Tag oder Switch-Port prüfen."),
    "unknown_beacon": ("Relay: unbekanntes Relay-Signal ({ITEM.LASTVALUE2})", "AVERAGE",
                       "Ein Relay ohne denselben Schlüssel (NSRELAY_BEACON_KEY) oder ein gefälschtes Signal. Das Relay bleibt aktiv."),
}


def alarm_items():
    """Two items per alarm kind: the affected keys and whether the alarm is open (with the trigger)."""
    for kind, (tname, prio, tdesc) in ALARMS.items():
        keys_key = f"nsrelay.alarm.keys[{kind}]"
        dep(Item(f"Relay: Alarm {kind} betroffen", keys_key, f"$.alarms.{kind}.keys", vt="CHAR", tag="alarm", default="[]"))
        # the second function only pulls the affected keys into the trigger name ({ITEM.LASTVALUE2})
        dep(Item(f"Relay: Alarm {kind} offen", f"nsrelay.alarm.active[{kind}]", f"$.alarms.{kind}.active", tag="alarm",
                 default="0", triggers=[Trigger(f"last(KEY)>0 and length(last(/{T}/{keys_key}))>=0", tname, prio, tdesc)]))


def vlan_discovery():
    """The low-level discovery of the networks (VLANs) with one item prototype per value."""
    w("      discovery_rules:")
    w(f"        - uuid: {u('lld:nsrelay.vlan.discovery')}")
    w("          name: 'Relay: Netze (VLANs)'")
    w("          type: DEPENDENT")
    w("          key: nsrelay.vlan.discovery")
    w("          delay: '0'")
    w("          lifetime: 7d")
    w("          item_prototypes:")
    proto(Proto("Netz {#NAME} (VLAN {#VID}): empfangen pro Sekunde", "nsrelay.vlan.in.rate[{#VID}]", "in_per_s", units="fps"))
    proto(Proto("Netz {#NAME} (VLAN {#VID}): gesendet pro Sekunde", "nsrelay.vlan.out.rate[{#VID}]", "out", vt="FLOAT",
                units="fps", change=True))
    proto(Proto("Netz {#NAME} (VLAN {#VID}): Sekunden ohne Empfang", "nsrelay.vlan.silent_s[{#VID}]", "silent_s", units="s"))
    proto(Proto("Netz {#NAME} (VLAN {#VID}): still", "nsrelay.vlan.silent[{#VID}]", "silent",
                trig=Trigger("last(KEY)=1", "Relay: Netz {#NAME} (VLAN {#VID}) still seit 10 Minuten", "WARNING",
                             "Während eines aktiven Events kam auf diesem Netz kein Broadcast an.")))
    w("          master_item:")
    w("            key: nsrelay.monitoring")
    w("          lld_macro_paths:")
    for m, p in [("{#VID}", "$.vid"), ("{#NAME}", "$.name"), ("{#GROUP}", "$.group")]:
        w(f"            - lld_macro: {q(m)}")
        w(f"              path: {q(p)}")
    w("          preprocessing:")
    w("            - type: JSONPATH")
    w("              parameters:")
    w("                - $.vlan_list")


def template_tail():
    """The tags of the template and the macros a host has to set."""
    w("      tags:")
    w("        - tag: class")
    w("          value: application")
    w("        - tag: target")
    w("          value: nsrelay")
    w("      macros:")
    w("        - macro: '{$NSRELAY.TOKEN}'")
    w("          type: SECRET_TEXT")
    w("          description: 'Token von der System-Seite des Relays (Abschnitt Monitoring).'")
    w("        - macro: '{$NSRELAY.URL}'")
    w("          value: 'http://127.0.0.1:8080'")
    w("          description: 'Basis-URL der Relay-Weboberfläche (MGMT-Adresse und Port), ohne Schrägstrich am Ende.'")


def main():
    template_head()
    relay_items()
    alarm_items()
    vlan_discovery()
    template_tail()
    OUT.parent.mkdir(parents=True, exist_ok=True)
    OUT.write_text("\n".join(L) + "\n", encoding="utf-8", newline="\n")
    print(OUT)


if __name__ == "__main__":
    main()
