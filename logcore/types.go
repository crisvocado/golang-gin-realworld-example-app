package logcore

// RequestEnvelope represents the top-level HTTP payload sent to Logcore.
type RequestEnvelope struct {
	SchemaVersion int        `json:"schema_version"`
	Entries       []LogEntry `json:"entries"`
}

// LogEntry represents a single log entry formatted for Logcore HTTP transport.
type LogEntry struct {
	Timestamp string            `json:"timestamp"`
	Severity  string            `json:"severity"`
	Message   string            `json:"message"`
	Service   string            `json:"service"`
	Env       string            `json:"env"`
	InsertID  string            `json:"insert_id"`
	Trace     *TraceContext     `json:"trace,omitempty"`
	Error     *ErrorDetails     `json:"error,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Context   map[string]any    `json:"context,omitempty"`
}

// TraceContext represents W3C distributed trace context.
type TraceContext struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId,omitempty"`
	Sampled bool   `json:"sampled,omitempty"`
}

// ErrorDetails represents structured error / exception details.
type ErrorDetails struct {
	Type    string       `json:"type"`
	Message string       `json:"message"`
	Stack   []StackFrame `json:"stack,omitempty"`
}

// StackFrame represents a single frame in the error stack trace.
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	InApp    bool   `json:"inApp"`
}
