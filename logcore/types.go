package logcore

// RequestEnvelope is the top-level wire envelope sent via HTTP POST to /v1/logs.
type RequestEnvelope struct {
	SchemaVersion int         `json:"schema_version"`
	Entries       []*LogEntry `json:"entries"`
}

// LogEntry represents a single log entry adhering to the Logcore schema.
type LogEntry struct {
	Timestamp string                 `json:"timestamp"`
	Severity  string                 `json:"severity"`
	Message   string                 `json:"message"`
	Service   string                 `json:"service"`
	Env       string                 `json:"env"`
	InsertID  string                 `json:"insert_id"`
	Trace     *TraceContext          `json:"trace,omitempty"`
	Error     *ErrorInfo             `json:"error,omitempty"`
	Labels    map[string]string      `json:"labels,omitempty"`
	Context   map[string]interface{} `json:"context,omitempty"`
}

// TraceContext represents parsed W3C distributed trace context.
type TraceContext struct {
	TraceID string  `json:"traceId"`
	SpanID  *string `json:"spanId,omitempty"`
	Sampled bool    `json:"sampled"`
}

// ErrorInfo represents structured exception/error information.
type ErrorInfo struct {
	Type    string        `json:"type"`
	Message string        `json:"message"`
	Stack   []*StackFrame `json:"stack,omitempty"`
}

// StackFrame represents a single frame in the parsed call stack.
type StackFrame struct {
	Function string  `json:"function"`
	File     *string `json:"file,omitempty"`
	Line     *int    `json:"line,omitempty"`
	Column   *int    `json:"column,omitempty"`
	InApp    bool    `json:"inApp"`
}
