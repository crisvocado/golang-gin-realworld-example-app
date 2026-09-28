package logcore

import (
	"time"
)

// Envelope is the top-level HTTP payload sent to logcore gateway.
type Envelope struct {
	SchemaVersion int     `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

// Entry is a single log event conforming to logcore wire shape.
type Entry struct {
	Timestamp     string            `json:"timestamp"`
	Severity      string            `json:"severity"`
	Message       string            `json:"message"`
	Service       string            `json:"service"`
	Env           string            `json:"env"`
	SourceProject string            `json:"source_project,omitempty"`
	InsertID      string            `json:"insert_id"`
	Trace         *TraceInfo        `json:"trace,omitempty"`
	Error         *ErrorInfo        `json:"error,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Context       map[string]any    `json:"context,omitempty"`
}

// TraceInfo represents W3C trace context.
type TraceInfo struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Sampled bool   `json:"sampled"`
}

// StackFrame represents a single frame in the parsed error stack.
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	InApp    bool   `json:"inApp"`
}

// ErrorInfo represents structured error details.
type ErrorInfo struct {
	Type    string       `json:"type"`
	Message string       `json:"message"`
	Stack   []StackFrame `json:"stack,omitempty"`
}

// Config holds client configuration parameters.
type Config struct {
	Enabled       bool
	URL           string
	Key           string
	Service       string
	Env           string
	MinSeverity   string
	BatchSize     int
	FlushInterval time.Duration
	MaxBuffer     int
	MaxRetries    int
	HTTPTimeout   time.Duration
}
