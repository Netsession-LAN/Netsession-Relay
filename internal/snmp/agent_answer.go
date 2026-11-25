package snmp

import (
	"crypto/subtle"
	"net"
	"net/netip"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Answering the requests of the agent: the community or USM user is checked, then GET, GETNEXT and GETBULK are
// answered from the tree; SET is refused (not writable).

func (a *Agent) handle(p *gosnmp.SnmpPacket, remote *net.UDPAddr) {
	src, _ := netip.AddrFromSlice(remote.IP)
	if !a.allowed(src.Unmap()) || !a.authorized(p) {
		return
	}
	es := a.table()
	resp := *p
	resp.PDUType = gosnmp.GetResponse
	resp.Error, resp.ErrorIndex = gosnmp.NoError, 0
	if !a.answer(&resp, p, es) {
		return
	}
	_ = a.tl.SendUDP(&resp, remote)
}

// authorized checks the version and credentials of a request: v1/v2c need the configured community (a wrong
// one gets no answer), v3 needs a configured user. For v3 it also fills in the engine boots and time of the
// reply.
func (a *Agent) authorized(p *gosnmp.SnmpPacket) bool {
	switch p.Version {
	case gosnmp.Version2c, gosnmp.Version1:
		return a.c.V2c && subtle.ConstantTimeCompare([]byte(p.Community), []byte(a.c.Community)) == 1
	case gosnmp.Version3:
		return a.authorizedV3(p)
	}
	return false
}

func (a *Agent) authorizedV3(p *gosnmp.SnmpPacket) bool {
	if a.c.V3User == "" {
		return false
	}
	if sp, ok := p.SecurityParameters.(*gosnmp.UsmSecurityParameters); ok {
		sp.AuthoritativeEngineBoots = a.c.Boots
		sp.AuthoritativeEngineTime = uint32(time.Since(a.start).Seconds())
	}
	return true
}

// answer fills the variables (and for SET the error) of the response to request p; false for request types
// that get no answer.
func (a *Agent) answer(resp, p *gosnmp.SnmpPacket, es []entry) bool {
	switch p.PDUType {
	case gosnmp.GetRequest:
		resp.Variables = a.each(es, p.Variables, a.get)
	case gosnmp.GetNextRequest:
		resp.Variables = a.each(es, p.Variables, a.next)
	case gosnmp.GetBulkRequest:
		resp.Variables = a.bulk(es, p)
		resp.NonRepeaters, resp.MaxRepetitions = 0, 0
	case gosnmp.SetRequest:
		resp.Error, resp.ErrorIndex = gosnmp.NotWritable, 1
		resp.Variables = p.Variables
	default:
		return false
	}
	return true
}

// each resolves every requested variable with lookup (get or next).
func (a *Agent) each(es []entry, vars []gosnmp.SnmpPDU, lookup func([]entry, []int) gosnmp.SnmpPDU) []gosnmp.SnmpPDU {
	var out []gosnmp.SnmpPDU
	for _, v := range vars {
		out = append(out, lookup(es, parseOID(v.Name)))
	}
	return out
}

// bulk answers GETBULK: the non-repeaters once, then the repeaters row by row, at most maxRepetitions rows and
// (after the first variable) at most maxResponse bytes.
func (a *Agent) bulk(es []entry, p *gosnmp.SnmpPacket) []gosnmp.SnmpPDU {
	nonRep := min(int(p.NonRepeaters), len(p.Variables))
	out := a.each(es, p.Variables[:nonRep], a.next)
	reps := min(int(p.MaxRepetitions), maxRepetitions)
	cursors := make([][]int, 0, len(p.Variables)-nonRep)
	for _, v := range p.Variables[nonRep:] {
		cursors = append(cursors, parseOID(v.Name))
	}
	size := 0
	for r := 0; r < reps && len(cursors) > 0; r++ {
		var full bool
		if out, full = a.bulkRow(es, cursors, out, &size); full {
			break
		}
	}
	return out
}

// bulkRow appends the next variable of every repeater to out and moves the cursors on. full reports that the
// response reached maxResponse bytes (size counts them across rows); the variable that did not fit is left out.
func (a *Agent) bulkRow(es []entry, cursors [][]int, out []gosnmp.SnmpPDU, size *int) (_ []gosnmp.SnmpPDU, full bool) {
	for i, c := range cursors {
		pdu := a.next(es, c)
		*size += pduSize(pdu)
		if *size > maxResponse && len(out) > 0 {
			return out, true
		}
		out = append(out, pdu)
		cursors[i] = parseOID(pdu.Name)
	}
	return out, false
}
