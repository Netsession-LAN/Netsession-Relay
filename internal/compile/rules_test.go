package compile

import "testing"

func TestConflictCode(t *testing.T) {
	for conflict, want := range map[string]string{
		"source_mode conflict (a wants original; b wants relay), using original":          WarnSourceModeConflict,
		"dst_mode conflict (a wants keep; b wants subnet), using keep":                    WarnDstModeConflict,
		"ttl_mode conflict (a wants decrement; b wants keep), using keep":                 WarnTTLModeConflict,
		"source filter ignored (VLAN 10: a filter, b not), those VLANs stay unrestricted": WarnSourceFilterOpen,
	} {
		if got := conflictCode(conflict); got != want {
			t.Errorf("conflictCode(%q) = %s, want %s", conflict, got, want)
		}
	}
}
