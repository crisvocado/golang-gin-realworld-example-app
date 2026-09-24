package logcore

// TraceContext represents distributed tracing information.
type TraceContext struct {
	TraceID string `json:"traceId,omitempty"`
	SpanID  string `json:"spanId,omitempty"`
	Sampled bool   `json:"sampled,omitempty"`
}

// StackFrame represents a single parsed stack frame.
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	InApp    bool   `json:"inApp"`
}

// ErrorInfo represents structured error and traceback details.
type ErrorInfo struct {
	Type    string       `json:"type"`
	Message string       `json:"message"`
	Stack   []StackFrame `json:"stack,omitempty"`
}

// Entry represents a single structured log entry for Logcore.
type Entry struct {
	Timestamp     string                 `json:"timestamp"`
	Severity      string                 `json:"severity"`
	Message       string                 `json:"message"`
	Service       string                 `json:"service"`
	Env           string                 `json:"env"`
	SourceProject string                 `json:"source_project,omitempty"`
	InsertID      string                 `json:"insert_id"`
	Trace         *TraceContext          `json:"trace,omitempty"`
	Error         *ErrorInfo             `json:"error,omitempty"`
	Labels        map[string]string      `json:"labels,omitempty"`
	Context       map[string]interface{} `json:"context,omitempty"`
}

// RequestEnvelope wraps log entries sent to Logcore /v1/logs.
type RequestEnvelope struct {
	SchemaVersion int     `json:"schema_version"`
	Entries       []Entry `json:"entries"`
}

// Config holds runtime configuration for the Logcore client.
type Config struct {
	URL         string
	Key         string
	Enabled     bool
	Service     string
	Env         string
	MinSeverity string
}
