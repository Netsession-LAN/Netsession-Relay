package lab

import "bytes"

// The A2S_INFO exchange of a Steam/Source server with challenge, as the lab's fake server speaks it.
var (
	a2sQuery     = append([]byte{0xff, 0xff, 0xff, 0xff, 'T'}, []byte("Source Engine Query\x00")...)
	a2sChallenge = []byte{0x4c, 0x61, 0x62, 0x21}
)

// a2sAnswer is the answer to an A2S_INFO query payload: a query without challenge gets the challenge 'A', a
// query with the right challenge gets the info 'I' (name, map de_lab, 3/16 players; info is true). ok is false
// for anything else, which gets no answer.
func a2sAnswer(payload []byte, name string) (answer []byte, info, ok bool) {
	if !bytes.HasPrefix(payload, a2sQuery) {
		return nil, false, false
	}
	switch rest := payload[len(a2sQuery):]; {
	case len(rest) == 0:
		return append([]byte{0xff, 0xff, 0xff, 0xff, 'A'}, a2sChallenge...), false, true
	case bytes.Equal(rest, a2sChallenge):
		answer = []byte{0xff, 0xff, 0xff, 0xff, 'I', 17}
		for _, s := range []string{name, "de_lab", "cs2", "Counter-Strike 2"} {
			answer = append(append(answer, s...), 0)
		}
		return append(answer, 0xda, 0x02, 3, 16, 0), true, true
	}
	return nil, false, false
}
