package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// ShellHook runs shell commands in response to lifecycle events.
// Commands are mapped per event type and receive event context via
// environment variables prefixed with KUBEPORT_.
type ShellHook struct {
	name     string
	commands map[EventType]string // event -> shell command
	filter   map[string]bool     // nil = all services
}

// NewShellHook creates a shell hook from a map of event names to commands.
func NewShellHook(name string, commands map[string]string, filterServices []string) (*ShellHook, error) {
	cmds := make(map[EventType]string, len(commands))
	for eventName, cmd := range commands {
		et, ok := ParseEventType(eventName)
		if !ok {
			return nil, fmt.Errorf("unknown event %q", eventName)
		}
		cmds[et] = cmd
	}

	var filter map[string]bool
	if len(filterServices) > 0 {
		filter = make(map[string]bool, len(filterServices))
		for _, s := range filterServices {
			filter[s] = true
		}
	}

	return &ShellHook{
		name:     name,
		commands: cmds,
		filter:   filter,
	}, nil
}

func (h *ShellHook) Name() string { return h.name }

func (h *ShellHook) OnEvent(ctx context.Context, event Event) error {
	return h.run(ctx, event)
}

// Gate implements GateHook for synchronous pre-start hooks (e.g., VPN startup).
func (h *ShellHook) Gate(ctx context.Context, event Event) error {
	return h.run(ctx, event)
}

func (h *ShellHook) run(ctx context.Context, event Event) error {
	if h.filter != nil && event.Service != "" && !h.filter[event.Service] {
		return nil
	}

	cmd, ok := h.commands[event.Type]
	if !ok {
		return nil
	}

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Stdout = os.Stderr
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), eventEnv(event)...)

	if err := c.Run(); err != nil {
		return fmt.Errorf("shell hook %q command failed: %w", h.name, err)
	}
	return nil
}

func eventEnv(e Event) []string {
	env := []string{
		"KUBEPORT_EVENT=" + e.Type.String(),
		"KUBEPORT_SERVICE=" + e.Service,
		"KUBEPORT_LOCAL_PORT=" + strconv.Itoa(e.LocalPort),
		"KUBEPORT_REMOTE_PORT=" + strconv.Itoa(e.RemotePort),
		"KUBEPORT_POD=" + e.PodName,
		"KUBEPORT_RESTARTS=" + strconv.Itoa(e.Restarts),
	}
	if e.Error != nil {
		env = append(env, "KUBEPORT_ERROR="+e.Error.Error())
	}
	return env
}
