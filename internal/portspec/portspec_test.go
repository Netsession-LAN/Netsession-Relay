package portspec

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in    string
		ports []uint16
		str   string
	}{
		{"23757", []uint16{23757}, "23757"},
		{"27015-27020", []uint16{27015, 27016, 27017, 27018, 27019, 27020}, "27015-27020"},
		{"2302-2338/12", []uint16{2302, 2314, 2326, 2338}, "2302-2338/12"},
		{"2302-2340/12", []uint16{2302, 2314, 2326, 2338}, "2302-2340/12"},
		{" 6111 , 6112 ", []uint16{6111, 6112}, "6111,6112"},
		{"4242,26900-26905,27215", []uint16{4242, 26900, 26901, 26902, 26903, 26904, 26905, 27215}, "4242,26900-26905,27215"},
		{"1,65535", []uint16{1, 65535}, "1,65535"},
		{"65530-65535/2", []uint16{65530, 65532, 65534}, "65530-65535/2"},
		{"10,10,5", []uint16{5, 10}, "10,10,5"},
	}
	for _, tt := range tests {
		s, err := Parse(tt.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		if got := s.Ports(); !slices.Equal(got, tt.ports) {
			t.Errorf("Parse(%q).Ports() = %v, want %v", tt.in, got, tt.ports)
		}
		if got := s.String(); got != tt.str {
			t.Errorf("Parse(%q).String() = %q, want %q", tt.in, got, tt.str)
		}
		for _, p := range tt.ports {
			if !s.Contains(p) {
				t.Errorf("Parse(%q).Contains(%d) = false", tt.in, p)
			}
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", " ", "0", "65536", "-5", "5-", "20-10", "abc", "1,,2", "100/2", "1-10/0", "1-10/x", "1-2-3"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", in)
		}
	}
}

func TestContainsNotMember(t *testing.T) {
	s := MustParse("2302-2470/12,27015-27020")
	for _, p := range []uint16{2303, 2313, 2471, 27014, 27021, 1} {
		if s.Contains(p) {
			t.Errorf("Contains(%d) = true", p)
		}
	}
}

func TestFromPorts(t *testing.T) {
	tests := []struct {
		ports []uint16
		want  string
	}{
		{nil, ""},
		{[]uint16{23757}, "23757"},
		{[]uint16{27020, 27015, 27016, 27017, 27018, 27019}, "27015-27020"},
		{[]uint16{2302, 2314, 2326, 2338, 2350}, "2302-2350/12"},
		{[]uint16{6111, 6112, 6112}, "6111-6112"},
		{[]uint16{4242, 26900, 26901, 27215}, "4242,26900-26901,27215"},
		{[]uint16{10, 20}, "10,20"},
		{[]uint16{10, 20, 30, 31, 32}, "10-30/10,31-32"},
	}
	for _, tt := range tests {
		got := FromPorts(tt.ports)
		if got.String() != tt.want {
			t.Errorf("FromPorts(%v) = %q, want %q", tt.ports, got.String(), tt.want)
		}
		// round trip: same port set
		in := slices.Clone(tt.ports)
		slices.Sort(in)
		in = slices.Compact(in)
		if !slices.Equal(got.Ports(), in) {
			t.Errorf("FromPorts(%v).Ports() = %v", tt.ports, got.Ports())
		}
	}
}

func FuzzRoundTrip(f *testing.F) {
	f.Add("27015-27020,2302-2470/12,6112")
	f.Fuzz(func(t *testing.T, in string) {
		s, err := Parse(in)
		if err != nil {
			return
		}
		canon := FromPorts(s.Ports())
		again, err := Parse(canon.String())
		if err != nil {
			t.Fatalf("canonical %q does not parse: %v", canon, err)
		}
		if !slices.Equal(again.Ports(), s.Ports()) {
			t.Fatalf("round trip changed ports for %q", in)
		}
	})
}
