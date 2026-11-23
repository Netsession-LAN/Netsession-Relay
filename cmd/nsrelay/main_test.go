package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHealthAddr(t *testing.T) {
	for in, want := range map[string]string{":8080": "127.0.0.1:8080", "0.0.0.0:8080": "127.0.0.1:8080", "[::]:8080": "127.0.0.1:8080",
		"192.0.2.10:8080": "192.0.2.10:8080", "127.0.0.1:9000": "127.0.0.1:9000"} {
		if got := healthAddr(in); got != want {
			t.Errorf("healthAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"--version"}, 0, "nsrelay dev", ""},
		{"version subcommand", []string{"version"}, 0, "nsrelay dev", ""},
		{"help", []string{"help"}, 0, "Usage: nsrelay", ""},
		{"no args", nil, 2, "", "Usage: nsrelay"},
		{"unknown", []string{"bogus"}, 2, "", `unknown command "bogus"`},
		{"lab no args", []string{"lab"}, 2, "", "usage: nsrelay lab"},
		{"lab snapshot", []string{"lab", "snapshot"}, 0, "lab snapshot", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}
