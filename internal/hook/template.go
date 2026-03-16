package hook

import (
	"encoding/json"
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
// special characters may produce invalid JSON. Use ExpandVarsJSON for
// templates that embed variables inside JSON string literals, or use the
// default JSON payload (no body_template) for guaranteed well-formed output.
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

// ExpandVarsJSON is like ExpandVars but JSON-encodes each value before
// substitution, making it safe to embed variables inside JSON string literals.
// For example, if ${ERROR} contains a double-quote, it will be escaped as \".
//
// Use this when body_template contains variables inside JSON strings:
//
//	{"event": "${EVENT}", "error": "${ERROR}"}
func ExpandVarsJSON(s string, e Event) string {
	var errStr string
	if e.Error != nil {
		errStr = e.Error.Error()
	}
	replacements := []struct{ old, new string }{
		{"${EVENT}", jsonEncodeString(e.Type.String())},
		{"${SERVICE}", jsonEncodeString(e.Service)},
		{"${PARENT_NAME}", jsonEncodeString(e.ParentName)},
		{"${PORT_NAME}", jsonEncodeString(e.PortName)},
		{"${PORT}", strconv.Itoa(e.LocalPort)},
		{"${REMOTE_PORT}", strconv.Itoa(e.RemotePort)},
		{"${POD}", jsonEncodeString(e.PodName)},
		{"${RESTARTS}", strconv.Itoa(e.Restarts)},
		{"${ERROR}", jsonEncodeString(errStr)},
		{"${TIME}", e.Time.Format(time.RFC3339)},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	return s
}

// jsonEncodeString returns the JSON-escaped content of s without surrounding quotes.
// This is safe to embed directly inside a JSON string literal.
func jsonEncodeString(s string) string {
	b, _ := json.Marshal(s)
	// json.Marshal produces a quoted string like "\"foo\\\"bar\""; strip the outer quotes.
	return string(b[1 : len(b)-1])
}
