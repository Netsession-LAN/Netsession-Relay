package engine

import "golang.org/x/net/bpf"

// rxFilter passes only frames the relay may care about:
//   - destination MAC with group bit (broadcast or multicast) and either a relay beacon (EtherType 0x88B5) or
//     IPv4/UDP that is not a fragment,
//   - unicast IPv4/UDP (not a fragment) to ProbePort: answers to the server probe (probe.go).
//
// Offsets are for untagged Ethernet II (the socket is bound to a VLAN subinterface, the kernel removed the
// 802.1Q tag). Kept outside the linux file so the program can be tested with the BPF interpreter.
var rxFilter = []bpf.Instruction{
	/* 0 */ bpf.LoadAbsolute{Off: 0, Size: 1}, // dst MAC byte 0
	/* 1 */ bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x01, SkipFalse: 9}, // unicast -> 11
	/* 2 */ bpf.LoadAbsolute{Off: 12, Size: 2}, // EtherType
	/* 3 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: BeaconEtherType, SkipTrue: 5},
	/* 4 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x0800, SkipFalse: 5},
	/* 5 */ bpf.LoadAbsolute{Off: 23, Size: 1}, // IPv4 protocol
	/* 6 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipFalse: 3},
	/* 7 */ bpf.LoadAbsolute{Off: 20, Size: 2}, // flags + fragment offset
	/* 8 */ bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x3fff, SkipTrue: 1},
	/* 9 */ bpf.RetConstant{Val: 65535},
	/* 10 */ bpf.RetConstant{Val: 0},
	// unicast: only UDP answers to the server probe
	/* 11 */ bpf.LoadAbsolute{Off: 12, Size: 2},
	/* 12 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0x0800, SkipFalse: 8},
	/* 13 */ bpf.LoadAbsolute{Off: 23, Size: 1},
	/* 14 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: 17, SkipFalse: 6},
	/* 15 */ bpf.LoadAbsolute{Off: 20, Size: 2},
	/* 16 */ bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: 0x3fff, SkipTrue: 4},
	/* 17 */ bpf.LoadMemShift{Off: 14}, // X = IPv4 header length
	/* 18 */ bpf.LoadIndirect{Off: 16, Size: 2}, // UDP destination port
	/* 19 */ bpf.JumpIf{Cond: bpf.JumpEqual, Val: ProbePort, SkipFalse: 1},
	/* 20 */ bpf.RetConstant{Val: 65535},
	/* 21 */ bpf.RetConstant{Val: 0},
}
