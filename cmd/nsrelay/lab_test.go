package main

import (
	"bytes"
	"testing"
)

func TestLabPayload(t *testing.T) {
	for _, c := range []struct {
		text, hex string
		size      int
		want      []byte
		fails     bool
	}{
		{text: "FO2?", want: []byte("FO2?")},
		{text: "FO2?", hex: "ff00", want: []byte{0xff, 0}},
		{text: "FO2?", hex: "ff00", size: 3, want: []byte{0, 0, 0}},
		{hex: "zz", size: 3, fails: true}, // an invalid hex payload fails even when -size replaces it
	} {
		got, err := labPayload(c.text, c.hex, c.size)
		if (err != nil) != c.fails || !bytes.Equal(got, c.want) {
			t.Errorf("labPayload(%q, %q, %d) = %v, %v", c.text, c.hex, c.size, got, err)
		}
	}
}
