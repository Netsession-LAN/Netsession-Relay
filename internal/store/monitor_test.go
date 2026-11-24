package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckTarget(t *testing.T) {
	ok := Target{Kind: TargetSyslog, Name: "log", Endpoint: "udp://192.0.2.1:514", Events: []string{"alarm", "audit"}}
	if err := checkTarget(ok); err != nil {
		t.Fatalf("valid target: %v", err)
	}
	for name, tt := range map[string]struct {
		change func(*Target)
		want   string
	}{
		"kind":          {func(t *Target) { t.Kind = "mail" }, `kind "mail"`},
		"no name":       {func(t *Target) { t.Name = "" }, "name"},
		"long name":     {func(t *Target) { t.Name = strings.Repeat("n", 61) }, "name"},
		"no endpoint":   {func(t *Target) { t.Endpoint = "" }, "endpoint"},
		"long endpoint": {func(t *Target) { t.Endpoint = strings.Repeat("e", 301) }, "endpoint"},
		"event":         {func(t *Target) { t.Events = []string{"alarm", "debug"} }, `event "debug"`},
	} {
		tg := ok
		tt.change(&tg)
		err := checkTarget(tg)
		if !errors.Is(err, ErrInvalid) || err.Error() != "invalid: "+tt.want {
			t.Errorf("%s: %v, want ErrInvalid %q", name, err, tt.want)
		}
	}
}
