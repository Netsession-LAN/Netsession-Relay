package engine

import (
	"reflect"
	"testing"
)

func TestMergePorts(t *testing.T) {
	for _, c := range []struct {
		in   []uint16
		want []PortRange
	}{
		{nil, nil},
		{[]uint16{7}, []PortRange{{7, 7}}},
		{[]uint16{1, 2, 3, 5, 5, 6, 9}, []PortRange{{1, 3}, {5, 6}, {9, 9}}},
		{[]uint16{65534, 65535}, []PortRange{{65534, 65535}}},
		{[]uint16{0, 65535}, []PortRange{{0, 0}, {65535, 65535}}},
	} {
		if got := mergePorts(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("mergePorts(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
