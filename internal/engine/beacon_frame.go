package engine

import (
	"bytes"
	"encoding/binary"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The wire format of a relay beacon: building and parsing the frame.

// Beacon payload layout (after the Ethernet header). Version 1 ends after the hash; version 2 appends the flags,
// the number of port ranges and the ranges, optionally followed by the signature trailer (send time + tag).
const (
	beaconOffMagic    = 0  // 4 bytes "NSRL"
	beaconOffVersion  = 4  // 1 byte
	beaconOffInstance = 5  // 16 bytes
	beaconOffStarted  = 21 // 8 bytes, unix nanos
	beaconOffActive   = 29 // 1 byte, 1 = active
	beaconOffHash     = 30 // 8 bytes
	beaconOffFlags    = 38 // 1 byte (version 2)
	beaconOffCount    = 39 // 2 bytes, number of port ranges (version 2)
	beaconOffRanges   = 41 // beaconRangeBytes per range: lo, hi (version 2)

	beaconLen        = beaconOffFlags // version 1 payload
	beaconMaxRanges  = 300            // 300*4 bytes after the fixed part: fits a 1500-byte frame
	beaconFlagAll    = 1
	beaconFlagSigned = 2 // a signature trailer follows the port ranges (SignBeacon)
	beaconVersion    = 2
	beaconRangeBytes = 4
	beaconTimeLen    = 8                            // send time of a signed beacon, unix nanos
	beaconTagLen     = 16                           // truncated HMAC-SHA256
	beaconSigLen     = beaconTimeLen + beaconTagLen // signature trailer
	// BeaconMaxSkew is how far the send time of a signed beacon may be from this engine's clock. A recorded
	// beacon is worthless after it; two relays that guard each other need clocks closer than this (NTP).
	BeaconMaxSkew = 30 * time.Second
)

// Ethernet header offsets used for beacon frames.
const (
	ethOffSrc  = 6
	ethOffType = 12
)

// BuildBeacon returns a broadcast beacon frame sent from mac.
func BuildBeacon(mac pkt.MAC, b Beacon) []byte {
	n := len(b.Ports)
	if b.sendsAllPorts() {
		n = 0
	}
	f := make([]byte, pkt.EthHeaderLen+beaconOffRanges+n*beaconRangeBytes, 96+n*beaconRangeBytes) // padded by the NIC
	copy(f[0:ethOffSrc], pkt.BroadcastMAC[:])
	copy(f[ethOffSrc:ethOffType], mac[:])
	binary.BigEndian.PutUint16(f[ethOffType:], BeaconEtherType)
	p := f[pkt.EthHeaderLen:]
	copy(p[beaconOffMagic:], beaconMagic[:])
	p[beaconOffVersion] = beaconVersion
	copy(p[beaconOffInstance:beaconOffStarted], b.Instance[:])
	binary.BigEndian.PutUint64(p[beaconOffStarted:], uint64(b.Started))
	if b.Active {
		p[beaconOffActive] = 1
	}
	copy(p[beaconOffHash:beaconOffFlags], b.Hash[:])
	if b.sendsAllPorts() {
		p[beaconOffFlags] = beaconFlagAll
	}
	binary.BigEndian.PutUint16(p[beaconOffCount:], uint16(n))
	for i := 0; i < n; i++ {
		o := beaconOffRanges + i*beaconRangeBytes
		binary.BigEndian.PutUint16(p[o:o+2], b.Ports[i].Lo)
		binary.BigEndian.PutUint16(p[o+2:o+4], b.Ports[i].Hi)
	}
	return f
}

// sendsAllPorts reports whether the beacon goes out as "all ports": ports unknown, or more ranges than fit.
func (b *Beacon) sendsAllPorts() bool {
	return b.AllPorts || len(b.Ports) > beaconMaxRanges
}

// ParseBeacon decodes a beacon frame (version 1 or 2).
func ParseBeacon(f []byte) (Beacon, bool) {
	var b Beacon
	version, ok := beaconFrameVersion(f)
	if !ok {
		return b, false
	}
	p := f[pkt.EthHeaderLen:]
	copy(b.Instance[:], p[beaconOffInstance:beaconOffStarted])
	b.Started = int64(binary.BigEndian.Uint64(p[beaconOffStarted:beaconOffActive]))
	b.Active = p[beaconOffActive] == 1
	copy(b.Hash[:], p[beaconOffHash:beaconOffFlags])
	if version == 1 {
		b.AllPorts = true
		return b, true
	}
	if !b.parsePorts(p) {
		return Beacon{}, false
	}
	return b, true
}

// beaconFrameVersion returns the beacon version of frame f; false if f is no beacon of a known version.
func beaconFrameVersion(f []byte) (byte, bool) {
	if len(f) < pkt.EthHeaderLen+beaconLen || binary.BigEndian.Uint16(f[ethOffType:]) != BeaconEtherType {
		return 0, false
	}
	p := f[pkt.EthHeaderLen:]
	version := p[beaconOffVersion]
	known := version == 1 || version == beaconVersion
	return version, known && bytes.Equal(p[beaconOffMagic:beaconOffVersion], beaconMagic[:])
}

// parsePorts reads the port part of a version 2 beacon payload p.
func (b *Beacon) parsePorts(p []byte) bool {
	if len(p) < beaconOffRanges {
		return false
	}
	b.AllPorts = p[beaconOffFlags]&beaconFlagAll != 0
	n := int(binary.BigEndian.Uint16(p[beaconOffCount:beaconOffRanges]))
	if n > beaconMaxRanges || len(p) < beaconOffRanges+n*beaconRangeBytes {
		return false
	}
	for i := 0; i < n; i++ {
		o := beaconOffRanges + i*beaconRangeBytes
		b.Ports = append(b.Ports, PortRange{binary.BigEndian.Uint16(p[o : o+2]), binary.BigEndian.Uint16(p[o+2 : o+4])})
	}
	return true
}
