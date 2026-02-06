package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// ExecHook runs a command with template-expanded arguments on lifecycle events.
// Template variables: ${EVENT}, ${SERVICE}, ${PORT}, ${REMOTE_PORT}, ${POD}, ${ERROR}.
type ExecHook struct {
	name    string
	command []string // template args, expanded per event
	filter  map[string]bool
}

// NewExecHook creates an exec hook.
func NewExecHook(name string, command []string, filterServices []string) (*ExecHook, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("exec hook %q: command is required", name)
	}
	var filter map[string]bool
	if len(filterServices) > 0 {
		filter = make(map[string]bool, len(filterServices))
		for _, s := range filterServices {
			filter[s] = true
		}
	}
	return &ExecHook{
		name:    name,
		command: command,
		filter:  filter,
	}, nil
}

func (h *ExecHook) Name() string { return h.name }

func (h *ExecHook) OnEvent(ctx context.Context, event Event) error {
	if h.filter != nil && event.Service != "" && !h.filter[event.Service] {
		return nil
	}

	args := make([]string, len(h.command))
	for i, tmpl := range h.command {
		args[i] = expandVars(tmpl, event)
	}

	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), eventEnv(event)...)

	if err := c.Run(); err != nil {
		return fmt.Errorf("exec hook %q failed: %w", h.name, err)
	}
	return nil
}

func expandVars(s string, e Event) string {
	var errStr string
	if e.Error != nil {
		errStr = e.Error.Error()
	}
	replacements := []struct{ old, new string }{
		{"${EVENT}", e.Type.String()},
		{"${SERVICE}", e.Service},
		{"${PORT}", strconv.Itoa(e.LocalPort)},
		{"${REMOTE_PORT}", strconv.Itoa(e.RemotePort)},
		{"${POD}", e.PodName},
		{"${ERROR}", errStr},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}
