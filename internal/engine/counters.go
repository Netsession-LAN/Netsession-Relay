package engine

import (
	"fmt"
	"sync/atomic"
)

// The reasons the pipeline gives for a frame, and the engine-wide counters.

// Reason is why a frame was not relayed (or Relayed).
type Reason int

// Reasons. Keep in sync with reasonNames.
const (
	Relayed Reason = iota
	DropParse
	DropOwnMAC
	DropBlocked
	DropForeignRelay
	DropSrcSubnet
	DropNotBroadcast
	DropUnknownPort
	DropNoTarget
	DropSize
	DropTTL
	DropMode // engine in observe/paused/standby/holdoff: nothing is sent
	DropLoop
	DropRate
	DropAutoPaused
	DropSrcFilter // the rule's source filter restricts the ingress VLAN and the sender is not on it
	// DropSendError: at least one copy of the frame could not be built or sent (interface down, socket error).
	// Unlike the other reasons it is also counted for a frame of which other copies did go out, so the reason
	// counters can add up to more than the frames received.
	DropSendError
	DropPanic // handling the frame panicked; the engine keeps running
	numReasons
)

var reasonNames = [numReasons]string{
	"relayed", "parse", "own_mac", "blocked", "foreign_relay", "src_subnet", "not_broadcast",
	"unknown_port", "no_target", "size", "ttl", "mode", "loop", "rate", "auto_paused",
	"src_filter", "send_error", "panic",
}

func (r Reason) String() string {
	if r < 0 || r >= numReasons {
		return fmt.Sprintf("reason(%d)", int(r))
	}
	return reasonNames[r]
}

// Counters are engine-wide statistics. All fields are safe for concurrent use.
type Counters struct {
	RX           atomic.Uint64
	Sent         atomic.Uint64 // frames sent (one received frame may produce several)
	Reasons      [numReasons]atomic.Uint64
	UnknownPorts [65536]atomic.Uint32      // broadcasts on ports without a rule (port finder)
	VLANIn       [MaxVID + 1]atomic.Uint64 // frames received per VLAN ID (after the kernel filter)
	VLANOut      [MaxVID + 1]atomic.Uint64 // frames sent per VLAN ID
}

// ReasonCounts returns the reason counters as a map (for logs and the API).
func (c *Counters) ReasonCounts() map[string]uint64 {
	m := map[string]uint64{}
	for i := range c.Reasons {
		if v := c.Reasons[i].Load(); v > 0 {
			m[Reason(i).String()] = v
		}
	}
	return m
}
