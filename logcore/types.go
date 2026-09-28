package logcore

import (
	"fmt"
)

// Severity levels supported by logcore
const (
	SeverityDebug    = "DEBUG"
	SeverityInfo     = "INFO"
	SeverityWarning  = "WARNING"
	SeverityError    = "ERROR"
	SeverityCritical = "CRITICAL"
)

// SeverityLevel maps severity strings to integer ranks for comparison.
var severityRanks = map[string]int{
	SeverityDebug:    100,
	SeverityInfo:     200,
	SeverityWarning:  300,
	SeverityError:    400,
	SeverityCritical: 500,
}

// Frame represents a single parsed stack frame.
// Field names follow the Logcore wire schema (inApp is camelCase).
type Frame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	InApp    bool   `json:"inApp"`
}

// ErrorInfo represents error metadata and parsed stack frames.
type ErrorInfo struct {
	Type    string  `json:"type"`
	Message string  `json:"message"`
	Stack   []Frame `json:"stack,omitempty"`
}

// Trace represents W3C distributed trace context.
type Trace struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Sampled bool   `json:"sampled"`
}

// Traceparent formats the trace context as a W3C traceparent header value.
func (t *Trace) Traceparent() string {
	if t == nil {
		return ""
	}
	flags := "00"
	if t.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", t.TraceID, t.SpanID, flags)
}

// Entry represents a single logcore log record matching the wire shape contract.
type Entry struct {
	Timestamp     string                 `json:"timestamp"`
	Severity      string                 `json:"severity"`
	Message       string                 `json:"message"`
	Service       string                 `json:"service"`
	Env           string                 `json:"env"`
	SourceProject string                 `json:"source_project,omitempty"`
	InsertID      string                 `json:"insert_id"`
	Trace         *Trace                 `json:"trace,omitempty"`
	Error         *ErrorInfo             `json:"error,omitempty"`
	Labels        map[string]string      `json:"labels,omitempty"`
	Context       map[string]interface{} `json:"context,omitempty"`
}

// Envelope is the JSON wrapper posted to the logcore HTTP ingest endpoint.
type Envelope struct {
	SchemaVersion int      `json:"schema_version"`
	Entries       []*Entry `json:"entries"`
}
