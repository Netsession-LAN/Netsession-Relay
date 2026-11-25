package demo

import (
	"fmt"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// The invented alarms of the demo engine, and which of them were acknowledged.

// demoAlarms invents the open alarms: a foreign relay in two VLANs for 50 s of every 90 s, a storm brake for
// 25 s of every 150 s.
func demoAlarms(s *compile.Snapshot, k tick) []engineapi.Alarm {
	return append(foreignRelayAlarms(s, k), stormAlarms(s, k)...)
}

func foreignRelayAlarms(s *compile.Snapshot, k tick) []engineapi.Alarm {
	since := int(k.t) % 90
	if since >= 50 || len(s.VLANs) <= 3 {
		return nil
	}
	var alarms []engineapi.Alarm
	for _, v := range s.VLANs[2:4] {
		alarms = append(alarms, engineapi.Alarm{Kind: "foreign_relay", Key: fmt.Sprint(v.VID), Count: uint64(40 + since*3),
			Detail: fmt.Sprintf("VLAN %d receives copies from 10.10.254.51 (02:00:00:00:00:51)", v.VID),
			First:  k.now.Add(-time.Duration(since) * time.Second), Last: k.now})
	}
	return alarms
}

func stormAlarms(s *compile.Snapshot, k tick) []engineapi.Alarm {
	p := int(k.t) % 150
	if p <= 100 || p >= 125 || len(s.Rules) == 0 {
		return nil
	}
	return []engineapi.Alarm{{Kind: "storm", Key: s.Rules[0].ID, Count: uint64(p - 100),
		Detail: fmt.Sprintf("rule %s: more than 500 packets/s, paused for 30 s", s.Rules[0].ID),
		First:  k.now.Add(-time.Duration(p-100) * time.Second), Last: k.now}}
}

// markAcked marks the alarms whose kind was acknowledged; when all is quiet the acknowledgements are dropped so
// the next incident is open again.
func (d *Engine) markAcked(alarms []engineapi.Alarm, now time.Time) {
	for i := range alarms {
		if by, ok := d.acked[alarms[i].Kind]; ok {
			alarms[i].AckedAt, alarms[i].AckedBy = now, by
		}
	}
	if len(alarms) == 0 {
		d.acked = map[string]string{}
	}
}
