package hook

import (
	"strconv"
	"strings"
	"time"
)

// ExpandVars replaces ${VAR} template variables in a string with event data.
// Supported variables: ${EVENT}, ${SERVICE}, ${PORT}, ${REMOTE_PORT}, ${POD},
// ${RESTARTS}, ${ERROR}, ${TIME}.
func ExpandVars(s string, e Event) string {
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
		{"${RESTARTS}", strconv.Itoa(e.Restarts)},
		{"${ERROR}", errStr},
		{"${TIME}", e.Time.Format(time.RFC3339)},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}
