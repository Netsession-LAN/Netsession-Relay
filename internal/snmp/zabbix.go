package snmp

import (
	"crypto/sha1" //nolint:gosec // G505: SHA-1 only derives a stable name-based UUID for the Zabbix template
	"encoding/hex"
	"fmt"
	"strings"
)

// The Zabbix template for the SNMP agent, deploy/zabbix/template_nsrelay_snmp.yaml (Zabbix 7.0 export format,
// imports into 7.x and 8). It is generated here, from the same object list as the MIB (`nsrelay snmp-mib
// -zabbix`), so OIDs and items cannot drift apart. The template for the HTTP endpoint is a different one with
// the same values and comes from tools/gen_zabbix_template.py. Never link both to one host (duplicate triggers).

// zabbixNS is the uuid5 namespace of tools/gen_zabbix_template.py, so shared objects (the template group) get
// the same UUID in both templates.
var zabbixNS = [16]byte{0x6f, 0x1d, 0x3c, 0x52, 0x0d, 0x1e, 0x4c, 0x55, 0x9a, 0x43, 0x6e, 0x5c, 0x1a, 0x2b, 0x7d, 0x10}

// zuuid is uuid5(zabbixNS, name) with the version/variant bits of a UUIDv4 (Zabbix only accepts v4 syntax).
func zuuid(name string) string {
	h := sha1.New() //nolint:gosec // G401: SHA-1 only derives a stable name-based UUID for the Zabbix template
	h.Write(zabbixNS[:])
	h.Write([]byte(name))
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b)
}

func zq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

const (
	zTemplate = "NS - Netsession Relay SNMP"
	zGroup    = "Templates/Netsession Relay" // template group of both templates; its UUID is derived from the name
)

// ZabbixTemplate returns the template as YAML.
func ZabbixTemplate() string {
	var z zabbixText
	z.header()
	for _, it := range zScalarItems {
		it.oid = OID(scalarNamed(it.scalar).Sub) + ".0"
		z.item(it)
	}
	z.alarmItems()
	z.trapItems()
	z.vlanDiscovery()
	z.w("      tags:")
	z.w("        - tag: class")
	z.w("          value: application")
	z.w("        - tag: target")
	z.w("          value: nsrelay")
	return z.b.String()
}

// zabbixText collects the lines of the template.
type zabbixText struct{ b strings.Builder }

// w writes one line.
func (z *zabbixText) w(format string, a ...any) { fmt.Fprintf(&z.b, format+"\n", a...) }

// zTrigger is a trigger on an item; KEY in expr stands for the item.
type zTrigger struct{ expr, name, prio, desc string }

// zItem is one SNMP item of the template. For the scalar items, scalar names the object and oid is filled in
// from it.
type zItem struct {
	label, key string
	scalar     string
	oid        string
	vt, units  string
	rate       bool
	triggers   []zTrigger
}

// zScalarItems are the items on scalar objects, in template order.
var zScalarItems = []zItem{
	{label: "Relay: Engine erreichbar", key: "nsrelay.snmp.engine.up", scalar: "nsRelayEngineUp", vt: "UNSIGNED",
		triggers: []zTrigger{{"last(KEY)=2", "Relay: Engine antwortet nicht", "HIGH", "nsRelayEngineUp ist false: die Web-Rolle erreicht die Engine nicht."}}},
	{label: "Relay: Betriebsart", key: "nsrelay.snmp.mode", scalar: "nsRelayMode", vt: "UNSIGNED"},
	{label: "Relay: Standby (zweites Relay)", key: "nsrelay.snmp.standby", scalar: "nsRelayStandby", vt: "UNSIGNED",
		triggers: []zTrigger{{"last(KEY)=1", "Relay: im Standby, ein anderes Relay ist aktiv", "AVERAGE", "Der Wächter hat ein zweites aktives Relay gesehen."}}},
	{label: "Relay: leitet weiter", key: "nsrelay.snmp.sending", scalar: "nsRelaySending", vt: "UNSIGNED"},
	{label: "Relay: Engine-Version", key: "nsrelay.snmp.engine.version", scalar: "nsRelayEngineVersion", vt: "CHAR"},
	{label: "Relay: Engine-Laufzeit", key: "nsrelay.snmp.uptime", scalar: "nsRelayEngineUptime", vt: "UNSIGNED"},
	{label: "Relay: Netze", key: "nsrelay.snmp.vlans", scalar: "nsRelayVlanCount", vt: "UNSIGNED"},
	{label: "Relay: Regeln", key: "nsrelay.snmp.rules", scalar: "nsRelayRuleCount", vt: "UNSIGNED"},
	{label: "Relay: offene Alarme", key: "nsrelay.snmp.alarms.active", scalar: "nsRelayAlarmsActive", vt: "UNSIGNED"},
	{label: "Relay: Monitoring-Ziele mit Fehler", key: "nsrelay.snmp.outputs.failing", scalar: "nsRelayOutputsFailing", vt: "UNSIGNED",
		triggers: []zTrigger{{"last(KEY)>0", "Relay: Monitoring-Ziel nicht erreichbar", "WARNING", "Ein Syslog-, OpenTelemetry- oder SNMP-Ziel nimmt seit 2 Minuten nichts an."}}},
	{label: "Relay: Selbsttest-Befunde", key: "nsrelay.snmp.selftest", scalar: "nsRelaySelfTestFindings", vt: "UNSIGNED",
		triggers: []zTrigger{{"last(KEY)>0", "Relay: Selbsttest meldet Befunde am Host", "INFO", "Details auf der System-Seite."}}},
	{label: "Relay: empfangen pro Sekunde", key: "nsrelay.snmp.rx.rate", scalar: "nsRelayRxFrames", vt: "FLOAT", units: "fps", rate: true},
	{label: "Relay: gesendet pro Sekunde", key: "nsrelay.snmp.sent.rate", scalar: "nsRelaySentFrames", vt: "FLOAT", units: "fps", rate: true},
}

// zAlarmTriggers is the trigger on the "alarm open" item of each alarm kind.
var zAlarmTriggers = map[string]zTrigger{
	"foreign_relay":  {"last(KEY)>0", "Relay: fremdes Relay aktiv", "HIGH", "Ein anderes Gerät leitet dieselben Broadcasts weiter."},
	"loop":           {"last(KEY)>0", "Relay: Schleife erkannt", "HIGH", "Dasselbe Paket kam auf mehreren VLANs an."},
	"storm":          {"last(KEY)>0", "Relay: Sturm-Bremse hat eine Regel pausiert", "AVERAGE", "Über 5000 Pakete/s; die lauteste Regel pausiert 60 s für das Netz, aus dem die Pakete kommen."},
	"unknown_beacon": {"last(KEY)>0", "Relay: unbekanntes Relay-Signal", "AVERAGE", "Ein Relay ohne denselben Schlüssel (NSRELAY_BEACON_KEY) oder ein gefälschtes Signal. Das Relay bleibt aktiv."},
	"dual_active":    {"last(KEY)>0", "Relay: zweites aktives Relay entdeckt", "HIGH", "Zwei Relays für dieselben Netze."},
	"vlan_silent":    {"last(KEY)>0", "Relay: Netz ohne Broadcasts seit 10 Minuten", "WARNING", "Während eines aktiven Events kam auf einem Netz nichts an."},
	"engine_down":    {"last(KEY)>0", "Relay: Engine weg (SNMP)", "HIGH", "Die Web-Rolle erreicht die Engine nicht."},
}

// scalarNamed returns the scalar object with the given name; an unknown name is a programming error.
func scalarNamed(name string) Scalar {
	for _, s := range Scalars {
		if s.Name == name {
			return s
		}
	}
	panic(name)
}

// header writes the export header, the template group and the template up to its item list.
func (z *zabbixText) header() {
	z.w("zabbix_export:")
	z.w("  version: '7.0'")
	z.w("  template_groups:")
	z.w("    - uuid: %s", zuuid("group:"+zGroup))
	z.w("      name: %s", zGroup)
	z.w("  templates:")
	z.w("    - uuid: %s", zuuid("snmp-template"))
	z.w("      template: %s", zq(zTemplate))
	z.w("      name: %s", zq(zTemplate))
	z.w("      description: |")
	z.w("        Netsession Relay over SNMP (NETSESSION-RELAY-MIB). Alternative to the HTTP template \"NS - Netsession Relay\":")
	z.w("        never link both to one host. Needs an SNMP interface (v3 authPriv recommended) on the host.")
	z.w("        Generated by `nsrelay snmp-mib -zabbix`, do not edit by hand.")
	z.w("      groups:")
	z.w("        - name: %s", zGroup)
	z.w("      items:")
}

// item writes one SNMP item with its triggers.
func (z *zabbixText) item(it zItem) {
	z.w("        - uuid: %s", zuuid("snmp-item:"+it.key))
	z.w("          name: %s", zq(it.label))
	z.w("          type: SNMP_AGENT")
	z.w("          snmp_oid: %s", zq("get["+it.oid+"]"))
	z.w("          key: %s", zq(it.key))
	z.w("          delay: 1m")
	z.w("          history: 30d")
	z.w("          value_type: %s", it.vt)
	if it.vt == "CHAR" {
		z.w("          trends: '0'")
	}
	if it.units != "" {
		z.w("          units: %s", zq(it.units))
	}
	if it.rate {
		z.w("          preprocessing:")
		z.w("            - type: CHANGE_PER_SECOND")
		z.w("              parameters:")
		z.w("                - ''")
	}
	z.w("          tags:")
	z.w("            - tag: component")
	z.w("              value: relay")
	z.triggers(it)
}

// triggers writes the triggers of an item (nothing when it has none).
func (z *zabbixText) triggers(it zItem) {
	if len(it.triggers) == 0 {
		return
	}
	z.w("          triggers:")
	for i, t := range it.triggers {
		z.w("            - uuid: %s", zuuid(fmt.Sprintf("snmp-trigger:%s:%d", it.key, i)))
		z.w("              expression: %s", zq(strings.ReplaceAll(t.expr, "KEY", "/"+zTemplate+"/"+it.key)))
		z.w("              name: %s", zq(t.name))
		z.w("              priority: %s", t.prio)
		z.w("              description: %s", zq(t.desc))
	}
}

// alarmItems writes one "alarm open" item with its trigger per alarm kind (nsRelayAlarmActiveTable).
func (z *zabbixText) alarmItems() {
	for i, k := range alarmKindsSNMP {
		z.item(zItem{label: "Relay: Alarm " + k + " offen", key: "nsrelay.snmp.alarm.active[" + k + "]",
			oid: fmt.Sprintf("%s.1.3.%d", OID("1.4"), i+1), vt: "UNSIGNED", triggers: []zTrigger{zAlarmTriggers[k]}})
	}
}

// trapItems writes an SNMP trap item for each of the first three notifications.
func (z *zabbixText) trapItems() {
	for _, n := range Notifications[:3] {
		// snmptrapd without our MIB writes "SNMPv2-SMI::enterprises.32473.1.1.0.1"; (\s|$) keeps .0.1 from
		// matching .0.10
		sym := "enterprises." + strings.TrimPrefix(OID(n.Sub), "1.3.6.1.4.1.")
		key := `snmptrap["` + strings.ReplaceAll(sym, ".", `\.`) + `(\s|$)"]`
		z.w("        - uuid: %s", zuuid("snmp-trap:"+n.Name))
		z.w("          name: %s", zq("Relay: Trap "+n.Name))
		z.w("          type: SNMP_TRAP")
		z.w("          key: %s", zq(key))
		z.w("          delay: '0'")
		z.w("          history: 30d")
		z.w("          value_type: LOG")
		z.w("          trends: '0'")
		z.w("          description: %s", zq(n.Desc+" Braucht snmptrapd (ohne geladene MIB) und am Host eine SNMP-Schnittstelle mit der Absender-IP des Relays."))
		z.w("          tags:")
		z.w("            - tag: component")
		z.w("              value: trap")
	}
}

// zProto is an item prototype of the VLAN discovery on a column of nsRelayVlanTable.
type zProto struct {
	label, key string
	col        int
	vt, units  string
	rate       bool
	tr         *zTrigger
}

// zVLANProtos are the item prototypes per discovered VLAN.
var zVLANProtos = []zProto{
	{"Netz {#VLANNAME} (VLAN {#SNMPINDEX}): empfangen pro Sekunde", "nsrelay.snmp.vlan.in.rate[{#SNMPINDEX}]", 5, "FLOAT", "fps", true, nil},
	{"Netz {#VLANNAME} (VLAN {#SNMPINDEX}): gesendet pro Sekunde", "nsrelay.snmp.vlan.out.rate[{#SNMPINDEX}]", 6, "FLOAT", "fps", true, nil},
	{"Netz {#VLANNAME} (VLAN {#SNMPINDEX}): Sekunden ohne Empfang", "nsrelay.snmp.vlan.silent_s[{#SNMPINDEX}]", 8, "UNSIGNED", "s", false, nil},
	{"Netz {#VLANNAME} (VLAN {#SNMPINDEX}): still", "nsrelay.snmp.vlan.silent[{#SNMPINDEX}]", 9, "UNSIGNED", "", false,
		&zTrigger{"last(KEY)=1", "Relay: Netz {#VLANNAME} (VLAN {#SNMPINDEX}) still seit 10 Minuten", "WARNING", "Während eines aktiven Events kam auf diesem Netz kein Broadcast an."}},
}

// vlanDiscovery writes the low-level discovery of the VLANs with its item prototypes.
func (z *zabbixText) vlanDiscovery() {
	z.w("      discovery_rules:")
	z.w("        - uuid: %s", zuuid("snmp-lld:vlan"))
	z.w("          name: 'Relay: Netze (VLANs) über SNMP'")
	z.w("          type: SNMP_AGENT")
	z.w("          snmp_oid: %s", zq("discovery[{#VLANNAME},"+OID("1.5.1.2")+",{#VLANGROUP},"+OID("1.5.1.3")+"]"))
	z.w("          key: nsrelay.snmp.vlan.discovery")
	z.w("          delay: 1h")
	z.w("          lifetime: 7d")
	z.w("          item_prototypes:")
	for _, p := range zVLANProtos {
		z.prototype(p)
	}
}

// prototype writes one item prototype with its trigger prototype.
func (z *zabbixText) prototype(p zProto) {
	z.w("            - uuid: %s", zuuid("snmp-proto:"+p.key))
	z.w("              name: %s", zq(p.label))
	z.w("              type: SNMP_AGENT")
	z.w("              snmp_oid: %s", zq(fmt.Sprintf("%s.1.%d.{#SNMPINDEX}", OID("1.5"), p.col)))
	z.w("              key: %s", zq(p.key))
	z.w("              delay: 1m")
	z.w("              history: 30d")
	z.w("              value_type: %s", p.vt)
	if p.units != "" {
		z.w("              units: %s", zq(p.units))
	}
	if p.rate {
		z.w("              preprocessing:")
		z.w("                - type: CHANGE_PER_SECOND")
		z.w("                  parameters:")
		z.w("                    - ''")
	}
	z.w("              tags:")
	z.w("                - tag: vlan")
	z.w("                  value: '{#SNMPINDEX} {#VLANNAME}'")
	if p.tr == nil {
		return
	}
	z.w("              trigger_prototypes:")
	z.w("                - uuid: %s", zuuid("snmp-trigproto:"+p.key))
	z.w("                  expression: %s", zq(strings.ReplaceAll(p.tr.expr, "KEY", "/"+zTemplate+"/"+p.key)))
	z.w("                  name: %s", zq(p.tr.name))
	z.w("                  priority: %s", p.tr.prio)
	z.w("                  description: %s", zq(p.tr.desc))
}
