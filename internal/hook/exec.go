package hook

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

var _ Hook = (*ExecHook)(nil)

// ExecHook runs a command with template-expanded arguments on lifecycle events.
// Template variables: ${EVENT}, ${SERVICE}, ${PORT}, ${REMOTE_PORT}, ${POD},
// ${RESTARTS}, ${ERROR}, ${TIME}.
type ExecHook struct {
	name    string
	command []string // template args, expanded per event
	filter  map[string]bool
}

// NewExecHook creates an exec hook.
func NewExecHook(name string, command, filterServices []string) (*ExecHook, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("exec hook %q: command is required", name)
	}
	return &ExecHook{
		name:    name,
		command: command,
		filter:  buildFilter(filterServices),
	}, nil
}

func (h *ExecHook) Name() string { return h.name }

func (h *ExecHook) OnEvent(ctx context.Context, event Event) error {
	if h.filter != nil && event.Service != "" && !h.filter[event.Service] {
		return nil
	}

	args := make([]string, len(h.command))
	for i, tmpl := range h.command {
		args[i] = ExpandVars(tmpl, event)
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
