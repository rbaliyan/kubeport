package hook

import (
	"strconv"
	"strings"
	"time"
)

// ExpandVars replaces ${VAR} template variables in a string with event data.
// Supported variables: ${EVENT}, ${SERVICE}, ${PARENT_NAME}, ${PORT_NAME},
// ${PORT}, ${REMOTE_PORT}, ${POD}, ${RESTARTS}, ${ERROR}, ${TIME}.
//
// Values are substituted as-is without escaping. When used inside a JSON
// body template (e.g., webhook body_template), values containing quotes or
// special characters may produce invalid JSON. Use the default JSON payload
// (no body_template) for guaranteed well-formed output.
func ExpandVars(s string, e Event) string {
	var errStr string
	if e.Error != nil {
		errStr = e.Error.Error()
	}
	replacements := []struct{ old, new string }{
		{"${EVENT}", e.Type.String()},
		{"${SERVICE}", e.Service},
		{"${PARENT_NAME}", e.ParentName},
		{"${PORT_NAME}", e.PortName},
		{"${PORT}", strconv.Itoa(e.LocalPort)},
		{"${REMOTE_PORT}", strconv.Itoa(e.RemotePort)},
		{"${POD}", e.PodName},
		{"${RESTARTS}", strconv.Itoa(e.Restarts)},
		{"${ERROR}", errStr},
		{"${TIME}", e.Time.Format(time.RFC3339)},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}
