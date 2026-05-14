package cli

import (
	"log/slog"
	"testing"

	"github.com/rbaliyan/kubeport/pkg/config"
)

func TestParseLogLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    slog.Level
		wantOK  bool
	}{
		{"", 0, false},
		{"debug", slog.LevelDebug, true},
		{"DEBUG", slog.LevelDebug, true},
		{" info ", slog.LevelInfo, true},
		{"warn", slog.LevelWarn, true},
		{"warning", slog.LevelWarn, true},
		{"error", slog.LevelError, true},
		{"trace", 0, false},
		{"nonsense", 0, false},
	}
	for _, tc := range cases {
		got, ok := parseLogLevel(tc.in)
		if ok != tc.wantOK || (ok && got != tc.want) {
			t.Errorf("parseLogLevel(%q) = (%v, %v); want (%v, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestResolveLogLevel(t *testing.T) {
	cases := []struct {
		name    string
		verbose bool
		cfgLvl  string
		want    slog.Level
	}{
		{"default", false, "", slog.LevelInfo},
		{"verbose wins over empty", true, "", slog.LevelDebug},
		{"verbose wins over warn", true, "warn", slog.LevelDebug},
		{"config debug", false, "debug", slog.LevelDebug},
		{"config error", false, "error", slog.LevelError},
		{"invalid falls back to info", false, "trace", slog.LevelInfo},
	}
	for _, tc := range cases {
		a := &app{verbose: tc.verbose, cfg: &config.Config{LogLevel: tc.cfgLvl}}
		if got := a.resolveLogLevel(); got != tc.want {
			t.Errorf("%s: resolveLogLevel = %v; want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveLogLevelNilConfig(t *testing.T) {
	a := &app{verbose: false}
	if got := a.resolveLogLevel(); got != slog.LevelInfo {
		t.Errorf("nil config: got %v; want Info", got)
	}
	a.verbose = true
	if got := a.resolveLogLevel(); got != slog.LevelDebug {
		t.Errorf("nil config + verbose: got %v; want Debug", got)
	}
}
