package engine

import (
	"bytes"
	"encoding/binary"
	"strconv"
	"strings"
)

// Parsing the info answers of game servers (Source A2S_INFO, Quake 3 getinfo) into what the players page shows.

// a2sInfo is the decoded part of an A2S_INFO answer.
type a2sAnswer struct {
	name, mp, game string
	num, mx        int
}

// parseA2SInfo reads name, map, game and player counts of an A2S_INFO answer (after the 'I' header byte).
func parseA2SInfo(b []byte) (a a2sAnswer, ok bool) {
	var str [4][]byte // NUL-terminated: name, map, folder, game
	if len(b) < 1 {
		return a, false
	}
	b = b[1:] // protocol
	for i := range str {
		if str[i], b, ok = bytes.Cut(b, []byte{0}); !ok {
			return a, false
		}
	}
	if len(b) < 4 {
		return a, false
	}
	_ = binary.LittleEndian.Uint16(b[:2]) // app id
	return a2sAnswer{name: clean(string(str[0])), mp: clean(string(str[1])), game: clean(string(str[3])), num: int(b[2]), mx: int(b[3])}, true
}

// parseQ3Info reads an id Tech 3 info string ("\hostname\x\mapname\y\clients\3\sv_maxclients\16").
func parseQ3Info(b []byte) (a a2sAnswer, ok bool) {
	parts := strings.Split(strings.TrimSpace(string(b)), "\\")
	if len(parts) < 3 {
		return a, false
	}
	kv := map[string]string{}
	for i := 1; i+1 < len(parts); i += 2 {
		kv[strings.ToLower(parts[i])] = parts[i+1]
	}
	num, _ := strconv.Atoi(kv["clients"])
	mx, _ := strconv.Atoi(kv["sv_maxclients"])
	return a2sAnswer{name: clean(stripQ3Colors(kv["hostname"])), mp: clean(kv["mapname"]), num: num, mx: mx}, true
}

// stripQ3Colors removes the ^N colour codes of id Tech 3 names.
func stripQ3Colors(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '^' && i+1 < len(s) {
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// clean keeps printable text and limits the length (names come from the network).
func clean(s string) string {
	r := []rune(s)
	out := make([]rune, 0, len(r))
	for _, c := range r {
		if printable(c) {
			out = append(out, c)
		}
		if len(out) >= 64 {
			break
		}
	}
	return string(out)
}

// printable reports whether c is shown as is: no control character and no replacement for invalid UTF-8.
func printable(c rune) bool {
	return c >= 0x20 && c != 0x7f && c != 0xfffd
}
