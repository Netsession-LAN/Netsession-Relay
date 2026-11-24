//go:build linux

package engine

import (
	"encoding/binary"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// receiver owns one packet socket and its goroutine.
type receiver struct {
	sock *packetSock
	vid  atomic.Int32 // VLAN ID of the interface; apply may change it while the goroutine reads it
	stop atomic.Bool
	done chan struct{}
}

func newReceiver(sock *packetSock, vid int) *receiver {
	r := &receiver{sock: sock, done: make(chan struct{})}
	r.vid.Store(int32(vid))
	return r
}

// halt stops the receive goroutine, waits for it and closes the socket. Other receivers may still hold the
// state from before the swap and send to this socket for a moment: packetSock refuses writes after close.
func (r *receiver) halt() {
	r.stop.Store(true)
	<-r.done
	r.sock.close()
}

func (e *Engine) receive(r *receiver) {
	defer e.wg.Done()
	defer close(r.done)
	buf := make([]byte, 2048)
	p := NewProcessor(&e.Counters, e.guard)
	for !r.stop.Load() {
		n, ok, err := r.sock.read(buf)
		if err != nil {
			e.readFailed(r, err)
			continue
		}
		if ok {
			e.handle(r, p, buf[:n])
		}
	}
}

// readFailed logs a failed read and slows the loop down, unless the receiver is being stopped.
func (e *Engine) readFailed(r *receiver, err error) {
	if r.stop.Load() {
		return
	}
	e.Log.Error("receive", "if", r.sock.name, "err", err)
	time.Sleep(time.Second)
}

// handle dispatches one received frame: beacons to the dual-active guard, unicast to the server probe, the
// broadcasts and multicasts to the processor.
func (e *Engine) handle(r *receiver, p *Processor, frame []byte) {
	if isBeacon(frame) {
		if e.guard.SeeFrame(frame, int(r.vid.Load()), time.Now()) {
			e.Log.Warn("dual-active guard: another active relay started earlier, going to STANDBY", "if", r.sock.name)
		}
		return
	}
	// A receiver can run before any state exists, and a network with constant broadcasts delivers a frame
	// in exactly that moment: such a frame is skipped.
	st := e.st.Load()
	if st == nil {
		return
	}
	in, known := st.index[r.sock]
	if !known {
		return
	}
	if len(frame) > 0 && frame[0]&1 == 0 { // unicast passes the filter only as an answer to the server probe
		e.followUpProbe(r, &st.table.VLANs[in], frame)
		return
	}
	e.processSafe(p, st, in, frame)
}

func isBeacon(frame []byte) bool {
	return len(frame) >= pkt.EthHeaderLen && binary.BigEndian.Uint16(frame[ethOffType:]) == BeaconEtherType
}

// followUpProbe sends what an answer to the server probe asks for (a second query), if anything.
func (e *Engine) followUpProbe(r *receiver, v *VLANRuntime, frame []byte) {
	out := e.probeReplySafe(v, frame)
	if out == nil {
		return
	}
	if err := r.sock.write(out); err != nil {
		e.Log.Debug("probe follow-up", "if", r.sock.name, "err", err)
	}
}

// probeReplySafe handles one answer to the server probe; like processSafe a panic only costs this frame.
func (e *Engine) probeReplySafe(v *VLANRuntime, frame []byte) (out []byte) {
	defer func() {
		if r := recover(); r != nil {
			e.Counters.Reasons[DropPanic].Add(1)
			e.Log.Error("panic while handling a probe answer (frame dropped)", "panic", r, "vlan", v.VID, "len", len(frame))
			out = nil
		}
	}()
	return e.guard.Probe.Reply(v, frame, time.Now())
}

// processSafe handles one frame; a panic is logged and counted instead of taking down the whole engine.
func (e *Engine) processSafe(p *Processor, st *state, in int, frame []byte) {
	defer func() {
		if r := recover(); r != nil {
			e.Counters.Reasons[DropPanic].Add(1)
			e.Log.Error("panic while processing a frame (frame dropped)", "panic", r, "vlan_index", in, "len", len(frame))
		}
	}()
	p.Process(st.table, in, frame, func(out int, b []byte) error {
		err := st.out[out].write(b)
		if err != nil && !errors.Is(err, errSockClosed) { // a socket closed by an apply is no news
			e.Log.Debug("send", "if", st.out[out].name, "err", err)
		}
		return err
	})
}
