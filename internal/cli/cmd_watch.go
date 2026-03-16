package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	version "github.com/rbaliyan/go-version"
	kubeportv1 "github.com/rbaliyan/kubeport/api/kubeport/v1"
)

// Box-drawing characters
const (
	boxTopLeft     = "┌"
	boxTopRight    = "┐"
	boxBottomLeft  = "└"
	boxBottomRight = "┘"
	boxHorizontal  = "─"
	boxVertical    = "│"
)

// ansiRegex matches ANSI escape sequences for stripping when calculating visible width.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// visibleLen returns the visible (printed) length of a string, ignoring ANSI escape codes.
func visibleLen(s string) int {
	return utf8.RuneCountInString(ansiRegex.ReplaceAllString(s, ""))
}

// drawBox wraps lines of text in a Unicode box border, padding each line to uniform width.
func drawBox(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	// Find max visible width
	maxWidth := 0
	for _, line := range lines {
		if w := visibleLen(line); w > maxWidth {
			maxWidth = w
		}
	}

	var buf bytes.Buffer
	hr := strings.Repeat(boxHorizontal, maxWidth+2)
	// Top border
	fmt.Fprintf(&buf, "%s%s%s%s%s\n", colorCyan, boxTopLeft, hr, boxTopRight, colorReset)
	// Content lines padded to uniform width
	for _, line := range lines {
		pad := maxWidth - visibleLen(line)
		fmt.Fprintf(&buf, "%s%s%s %s%s %s%s%s\n", colorCyan, boxVertical, colorReset, line, strings.Repeat(" ", pad), colorCyan, boxVertical, colorReset)
	}
	// Bottom border
	fmt.Fprintf(&buf, "%s%s%s%s%s\n", colorCyan, boxBottomLeft, hr, boxBottomRight, colorReset)
	return buf.String()
}

func (a *app) cmdWatch() {
	interval := a.watchInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}

	// Initial connection check
	dc, err := a.dialTarget()
	if dc != nil {
		dc.Close()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to daemon: %v\n", err)
		os.Exit(1)
	}
	if dc == nil {
		fmt.Fprintf(os.Stderr, "Proxy is not running\n")
		os.Exit(1)
	}

	// Handle Ctrl+C via signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Handle 'q' keypress — put terminal into raw mode for input only.
	// We render into a buffer and convert \n to \r\n on output so that
	// raw mode's disabled output processing doesn't break line formatting.
	quitCh := make(chan struct{})
	fd := int(os.Stdin.Fd())
	rawMode := false
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			rawMode = true
			defer func() { _ = term.Restore(fd, oldState) }()
			go func() {
				buf := make([]byte, 1)
				for {
					n, err := os.Stdin.Read(buf)
					if err != nil || n == 0 {
						return
					}
					if buf[0] == 'q' || buf[0] == 'Q' || buf[0] == 3 /* Ctrl+C */ {
						close(quitCh)
						return
					}
				}
			}()
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Render immediately, then on each tick
	a.renderWatch(interval, rawMode)
	for {
		select {
		case <-sigCh:
			return
		case <-quitCh:
			return
		case <-ticker.C:
			a.renderWatch(interval, rawMode)
		}
	}
}

// writeRaw writes buf to stdout, converting \n to \r\n when in raw terminal mode.
func writeRaw(buf []byte, rawMode bool) {
	if rawMode {
		buf = bytes.ReplaceAll(buf, []byte("\n"), []byte("\r\n"))
	}
	_, _ = os.Stdout.Write(buf)
}

func (a *app) renderWatch(interval time.Duration, rawMode bool) {
	var out bytes.Buffer

	// Clear terminal
	out.WriteString("\033[2J\033[H")

	// Build the content that goes inside the box
	var content bytes.Buffer

	dc, err := a.dialTarget()
	if err != nil {
		fmt.Fprintf(&content, "Error connecting to daemon: %v", err)
		out.WriteString(drawBox(content.String()))
		writeRaw(out.Bytes(), rawMode)
		return
	}
	if dc == nil {
		fmt.Fprintf(&content, "Every %s: kubeport status    %s\n", interval, time.Now().Format("Mon Jan 2 15:04:05 2006"))
		fmt.Fprintf(&content, "\nStatus: %sStopped%s", colorRed, colorReset)
		out.WriteString(drawBox(content.String()))
		writeRaw(out.Bytes(), rawMode)
		return
	}
	defer dc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := dc.client.Status(ctx, &kubeportv1.StatusRequest{})
	if err != nil {
		fmt.Fprintf(&content, "Every %s: kubeport status    %s\n", interval, time.Now().Format("Mon Jan 2 15:04:05 2006"))
		fmt.Fprintf(&content, "\nStatus: %sError%s: %v", colorRed, colorReset, err)
		out.WriteString(drawBox(content.String()))
		writeRaw(out.Bytes(), rawMode)
		return
	}

	if a.statusSort {
		slices.SortFunc(resp.Forwards, func(a, b *kubeportv1.ForwardStatusProto) int {
			return strings.Compare(a.Service.GetName(), b.Service.GetName())
		})
	}

	cliVer := version.Get().Raw
	daemonVer := resp.Version

	fmt.Fprintf(&content, "Every %s: kubeport status    %s\n", interval, time.Now().Format("Mon Jan 2 15:04:05 2006"))
	fmt.Fprintf(&content, "\n%sProxy Status%s\n", colorCyan, colorReset)
	fmt.Fprintf(&content, "\nStatus: %sRunning%s (gRPC)", colorGreen, colorReset)
	if cliVer != "" || daemonVer != "" {
		fmt.Fprintf(&content, "\n\nCLI version:    %s", cliVer)
		fmt.Fprintf(&content, "\nDaemon version: %s", daemonVer)
		if cliVer != "" && daemonVer != "" && cliVer != daemonVer {
			fmt.Fprintf(&content, "\n%sWarning: CLI and daemon versions differ — consider restarting the daemon%s", colorYellow, colorReset)
		}
	}
	fmt.Fprintf(&content, "\n\nContext:   %s", resp.Context)
	fmt.Fprintf(&content, "\nNamespace: %s", resp.Namespace)
	if a.configFile != "" {
		fmt.Fprintf(&content, "\nConfig:    %s", a.configFile)
	}

	if len(resp.Forwards) > 0 {
		fmt.Fprintf(&content, "\n\nForwards:")
		for _, fw := range resp.Forwards {
			content.WriteByte('\n')
			writeForwardStatus(&content, fw)
		}
	}

	fmt.Fprintf(&content, "\n\nPress 'q' to exit")

	out.WriteString(drawBox(content.String()))
	writeRaw(out.Bytes(), rawMode)
}

// writeForwardStatus writes a single forward's status line to w.
func writeForwardStatus(w io.Writer, fw *kubeportv1.ForwardStatusProto) {
	var stateColor, stateText, indicator string

	switch fw.State {
	case kubeportv1.ForwardState_FORWARD_STATE_RUNNING:
		stateColor = colorGreen
		stateText = "running"
		indicator = "●"
	case kubeportv1.ForwardState_FORWARD_STATE_STARTING:
		stateColor = colorYellow
		stateText = "starting"
		indicator = "◌"
	case kubeportv1.ForwardState_FORWARD_STATE_FAILED:
		stateColor = colorRed
		stateText = "failed"
		indicator = "✗"
	case kubeportv1.ForwardState_FORWARD_STATE_STOPPED:
		stateColor = colorRed
		stateText = "stopped"
		indicator = "○"
	default:
		stateColor = colorYellow
		stateText = "unknown"
		indicator = "?"
	}

	name := fw.Service.GetName()
	port := fw.ActualPort
	remotePort := fw.Service.GetRemotePort()

	if port > 0 {
		fmt.Fprintf(w, "  %s%s%s %s: localhost:%d -> :%d [%s%s%s]",
			stateColor, indicator, colorReset,
			name, port, remotePort,
			stateColor, stateText, colorReset)
	} else {
		fmt.Fprintf(w, "  %s%s%s %s: :%d [%s%s%s]",
			stateColor, indicator, colorReset,
			name, remotePort,
			stateColor, stateText, colorReset)
	}

	if fw.Restarts > 0 {
		fmt.Fprintf(w, " (restarts: %d)", fw.Restarts)
	}
	if fw.BytesIn > 0 || fw.BytesOut > 0 {
		fmt.Fprintf(w, " (in: %s, out: %s", formatBytes(fw.BytesIn), formatBytes(fw.BytesOut))
		if fw.State == kubeportv1.ForwardState_FORWARD_STATE_RUNNING && fw.LastStart != nil && fw.LastStart.IsValid() {
			elapsed := time.Since(fw.LastStart.AsTime()).Seconds()
			if elapsed > 0 {
				totalBytes := fw.BytesIn + fw.BytesOut
				fmt.Fprintf(w, ", %s/s", formatBytes(int64(float64(totalBytes)/elapsed)))
			}
		}
		fmt.Fprint(w, ")")
	}

	if fw.Error != "" {
		fmt.Fprintf(w, "\n         %sERROR: %s%s", colorRed, fw.Error, colorReset)
	}

	if fw.NextRetry != nil && fw.NextRetry.IsValid() {
		remaining := time.Until(fw.NextRetry.AsTime())
		if remaining > 0 {
			fmt.Fprintf(w, "\n         Reconnecting in %s", remaining.Round(time.Second))
		} else {
			fmt.Fprintf(w, "\n         Reconnecting now")
		}
	}
}
