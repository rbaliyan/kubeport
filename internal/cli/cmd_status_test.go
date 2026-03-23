package cli

import "testing"

func TestFormatBandwidth(t *testing.T) {
	tests := []struct {
		bytesPerSec int64
		want        string
	}{
		{125_000_000, "1.0 Gbps"},    // 1 Gbps
		{125_000, "1.0 Mbps"},        // 1 Mbps
		{62_500, "500.0 Kbps"},       // 500 Kbps
		{125, "1.0 Kbps"},            // 1 Kbps
		{10, "80 bps"},               // sub-Kbps
		{0, "0 bps"},                 // zero
		{1_250_000, "10.0 Mbps"},     // 10 Mbps
		{625_000_000, "5.0 Gbps"},    // 5 Gbps
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBandwidth(tt.bytesPerSec)
			if got != tt.want {
				t.Fatalf("formatBandwidth(%d) = %q, want %q", tt.bytesPerSec, got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Fatalf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}
