package snmp

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
)

// The variables the agent answers with: the system group and the relay tree, sorted by OID, and the lookups
// GET and GETNEXT do in them.

// entry is one variable: its OID as numbers and the PDU that answers it.
type entry struct {
	oid []int
	pdu gosnmp.SnmpPDU
}

func parseOID(s string) []int {
	parts := strings.Split(strings.TrimPrefix(s, "."), ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

func oidString(o []int) string {
	var b strings.Builder
	for _, n := range o {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(n))
	}
	return b.String()
}

// table returns all variables sorted by OID (cached for 5 s: one engine query for any number of requests).
func (a *Agent) table() []entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache != nil && time.Since(a.cachedAt) < 5*time.Second {
		return a.cache
	}
	es := a.variables(a.view())
	a.cache, a.cachedAt = es, time.Now()
	return es
}

// entries collects variables in any order.
type entries []entry

func (es *entries) add(oid string, typ gosnmp.Asn1BER, val any) {
	*es = append(*es, entry{oid: parseOID(oid), pdu: gosnmp.SnmpPDU{Name: "." + strings.TrimPrefix(oid, "."), Type: typ, Value: val}})
}

// variables returns the system group and the relay tree for view v, sorted by OID.
func (a *Agent) variables(v *View) []entry {
	var es entries
	a.addSystemGroup(&es)
	for _, s := range Scalars {
		es.add(OID(s.Sub)+".0", s.Syntax.BER, s.Get(v))
	}
	for _, t := range Tables {
		es.addTable(t, v)
	}
	slices.SortFunc(es, func(x, y entry) int { return slices.Compare(x.oid, y.oid) })
	return es
}

// addSystemGroup adds the variables of the MIB-2 system group.
func (a *Agent) addSystemGroup(es *entries) {
	sys := "1.3.6.1.2.1.1."
	es.add(sys+"1.0", gosnmp.OctetString, a.c.SysDescr)
	es.add(sys+"2.0", gosnmp.ObjectIdentifier, "."+Base)
	es.add(sys+"3.0", gosnmp.TimeTicks, uint32(time.Since(a.start)/(10*time.Millisecond)))
	es.add(sys+"4.0", gosnmp.OctetString, a.c.Contact)
	es.add(sys+"5.0", gosnmp.OctetString, a.c.SysName)
	es.add(sys+"6.0", gosnmp.OctetString, a.c.Location)
	es.add(sys+"7.0", gosnmp.Integer, 72)
}

// addTable adds every cell of table t, column by column.
func (es *entries) addTable(t Table, v *View) {
	rows := t.Rows(v)
	for _, c := range t.Columns {
		for _, r := range rows {
			es.add(fmt.Sprintf("%s.1.%d.%d", OID(t.Sub), c.Sub, r.Index), c.Syntax.BER, c.Get(r.Row))
		}
	}
}

func (a *Agent) get(es []entry, oid []int) gosnmp.SnmpPDU {
	i, found := slices.BinarySearchFunc(es, oid, func(e entry, o []int) int { return slices.Compare(e.oid, o) })
	if found {
		return es[i].pdu
	}
	return gosnmp.SnmpPDU{Name: oidString(oid), Type: gosnmp.NoSuchObject}
}

func (a *Agent) next(es []entry, oid []int) gosnmp.SnmpPDU {
	i, found := slices.BinarySearchFunc(es, oid, func(e entry, o []int) int { return slices.Compare(e.oid, o) })
	if found {
		i++
	}
	if i >= len(es) {
		return gosnmp.SnmpPDU{Name: oidString(oid), Type: gosnmp.EndOfMibView}
	}
	return es[i].pdu
}

func pduSize(p gosnmp.SnmpPDU) int {
	n := len(p.Name) + 8
	if s, ok := p.Value.(string); ok {
		n += len(s)
	}
	return n
}
