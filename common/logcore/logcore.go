package logcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// Envelope matches the Logcore batch request format.
type Envelope struct {
	SchemaVersion int        `json:"schema_version"`
	Entries       []LogEntry `json:"entries"`
}

// LogEntry matches the top-level wire shape for Logcore events.
type LogEntry struct {
	Timestamp string                 `json:"timestamp"`
	Severity  string                 `json:"severity"`
	Message   string                 `json:"message"`
	Service   string                 `json:"service"`
	Env       string                 `json:"env"`
	InsertID  string                 `json:"insert_id"`
	Trace     *TraceEntry            `json:"trace,omitempty"`
	Error     *ErrorEntry            `json:"error,omitempty"`
	Labels    map[string]string      `json:"labels,omitempty"`
	Context   map[string]interface{} `json:"context,omitempty"`
}

// TraceEntry represents distributed tracing context.
type TraceEntry struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Sampled bool   `json:"sampled"`
}

// ErrorEntry represents parsed error information.
type ErrorEntry struct {
	Type    string       `json:"type"`
	Message string       `json:"message"`
	Stack   []StackFrame `json:"stack"`
}

// StackFrame represents a single frame in the error stack trace.
type StackFrame struct {
	Function string `json:"function"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	InApp    bool   `json:"inApp"`
}

// Config configures the Logcore client transport.
type Config struct {
	URL         string
	Key         string
	Enabled     bool
	Service     string
	Env         string
	MinSeverity string
	HTTPClient  *http.Client
}

// Client delivers structured log events to Logcore gateway.
type Client struct {
	url            string
	ingestURL      string
	key            string
	enabled        bool
	service        string
	env            string
	minSeverity    string
	httpClient     *http.Client
	queue          chan LogEntry
	flushChan      chan chan struct{}
	stopChan       chan struct{}
	consecutiveErr int
	closed         bool
	mu             sync.Mutex
}

var severityLevels = map[string]int{
	"DEBUG":    100,
	"INFO":     200,
	"WARN":     300,
	"WARNING":  300,
	"ERROR":    400,
	"CRITICAL": 500,
	"FATAL":    600,
}

var (
	defaultClient     *Client
	defaultClientOnce sync.Once
)

// DefaultClient returns the singleton Logcore client initialized from environment variables.
func DefaultClient() *Client {
	defaultClientOnce.Do(func() {
		defaultClient = NewClientFromEnv()
	})
	return defaultClient
}

// NewClientFromEnv creates a new Logcore Client from environment variables.
func NewClientFromEnv() *Client {
	url := os.Getenv("LOGCORE_URL")
	key := os.Getenv("LOGCORE_KEY")
	enabledStr := os.Getenv("LOGCORE_ENABLED")
	service := os.Getenv("LOGCORE_SERVICE")
	if service == "" {
		service = "golang-gin-realworld-example-app"
	}
	env := os.Getenv("LOGCORE_ENV")
	if env == "" {
		env = "dev"
	}
	minSeverity := os.Getenv("LOGCORE_MIN_SEVERITY")
	if minSeverity == "" {
		minSeverity = "ERROR"
	}

	enabled := false
	if enabledStr == "true" || enabledStr == "1" {
		enabled = true
	} else if enabledStr == "" && url != "" && key != "" {
		enabled = true
	}

	return NewClient(Config{
		URL:         url,
		Key:         key,
		Enabled:     enabled,
		Service:     service,
		Env:         env,
		MinSeverity: minSeverity,
	})
}

// NewClient creates a new Logcore client with the provided config.
func NewClient(cfg Config) *Client {
	ingestURL := cfg.URL
	if ingestURL != "" {
		if !strings.HasSuffix(ingestURL, "/v1/logs") {
			ingestURL = strings.TrimRight(ingestURL, "/") + "/v1/logs"
		}
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	c := &Client{
		url:         cfg.URL,
		ingestURL:   ingestURL,
		key:         cfg.Key,
		enabled:     cfg.Enabled,
		service:     cfg.Service,
		env:         cfg.Env,
		minSeverity: cfg.MinSeverity,
		httpClient:  httpClient,
		queue:       make(chan LogEntry, 1000),
		flushChan:   make(chan chan struct{}),
		stopChan:    make(chan struct{}),
	}

	if c.enabled && c.ingestURL != "" {
		go c.worker()
	}

	return c
}

// IngestURL returns the ingest endpoint URL for self-exclusion.
func (c *Client) IngestURL() string {
	return c.ingestURL
}

// IsEnabled returns whether logging to Logcore is enabled.
func (c *Client) IsEnabled() bool {
	return c.enabled
}

// Emit queues a log entry for delivery to Logcore.
func (c *Client) Emit(entry LogEntry) {
	if c == nil || !c.enabled {
		return
	}

	if entry.Severity == "" {
		entry.Severity = "ERROR"
	}

	entryLevel := severityLevels[strings.ToUpper(entry.Severity)]
	minLevel := severityLevels[strings.ToUpper(c.minSeverity)]
	if minLevel == 0 {
		minLevel = severityLevels["ERROR"]
	}
	if entryLevel < minLevel {
		return
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if entry.Service == "" {
		entry.Service = c.service
	}
	if entry.Env == "" {
		entry.Env = c.env
	}
	if len(entry.Message) > 65536 {
		entry.Message = entry.Message[:65536]
	}
	if entry.InsertID == "" {
		entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	}
	if len(entry.Labels) > 32 {
		clampedLabels := make(map[string]string)
		count := 0
		for k, v := range entry.Labels {
			if count >= 32 {
				break
			}
			if len(v) > 1024 {
				v = v[:1024]
			}
			clampedLabels[k] = v
			count++
		}
		entry.Labels = clampedLabels
	}
	if entry.Error != nil && len(entry.Error.Stack) > 50 {
		entry.Error.Stack = entry.Error.Stack[:50]
	}

	select {
	case c.queue <- entry:
	default:
		// Drop oldest entry when buffer reaches capacity
		select {
		case <-c.queue:
		default:
		}
		select {
		case c.queue <- entry:
		default:
		}
	}
}

// Flush flushes buffered log entries to the server.
func (c *Client) Flush() {
	if c == nil || !c.enabled {
		return
	}
	reply := make(chan struct{})
	select {
	case c.flushChan <- reply:
		select {
		case <-reply:
		case <-time.After(5 * time.Second):
		}
	case <-time.After(5 * time.Second):
	}
}

// Close gracefully flushes remaining logs and stops the background worker.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	close(c.stopChan)
}

func (c *Client) worker() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var batch []LogEntry
	flush := func() {
		if len(batch) == 0 {
			return
		}
		c.sendBatch(batch)
		batch = nil
	}

	for {
		select {
		case entry, ok := <-c.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= 20 {
				flush()
			}
		case <-ticker.C:
			flush()
		case reply := <-c.flushChan:
			for {
				select {
				case entry, ok := <-c.queue:
					if ok {
						batch = append(batch, entry)
					}
				default:
					goto doneDraining
				}
			}
		doneDraining:
			flush()
			close(reply)
		case <-c.stopChan:
			for {
				select {
				case entry, ok := <-c.queue:
					if !ok {
						break
					}
					batch = append(batch, entry)
					if len(batch) >= 200 {
						flush()
					}
				default:
					goto drained
				}
			}
		drained:
			flush()
			return
		}
	}
}

func (c *Client) sendBatch(entries []LogEntry) {
	if len(entries) == 0 {
		return
	}

	c.mu.Lock()
	if c.consecutiveErr >= 10 {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	for i := 0; i < len(entries); i += 200 {
		end := i + 200
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]
		c.sendChunkWithRetry(chunk)
	}
}

func (c *Client) sendChunkWithRetry(chunk []LogEntry) {
	envelope := Envelope{
		SchemaVersion: 1,
		Entries:       chunk,
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcore: failed to marshal payload: %v\n", err)
		return
	}

	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest("POST", c.ingestURL, bytes.NewReader(payload))
		if err != nil {
			fmt.Fprintf(os.Stderr, "logcore: failed to create request: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.key)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt*250) * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			c.recordFailure()
			return
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			c.recordSuccess()
			return
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt*250) * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			c.recordFailure()
			return
		}

		fmt.Fprintf(os.Stderr, "logcore: ingest rejected with status %d: %s\n", resp.StatusCode, string(respBody))
		c.recordFailure()
		return
	}
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	c.consecutiveErr = 0
	c.mu.Unlock()
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	c.consecutiveErr++
	c.mu.Unlock()
}

// computeInsertID calculates a deterministic 32-character hex insert_id.
func computeInsertID(timestamp, service, severity, message string, context map[string]interface{}) string {
	var contextJSON string
	if len(context) > 0 {
		if b, err := json.Marshal(context); err == nil {
			contextJSON = string(b)
		}
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", timestamp, service, severity, message, contextJSON)
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])[:32]
}

// parseTraceparent parses a W3C traceparent header.
func parseTraceparent(header string) *TraceEntry {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	parts := strings.Split(header, "-")
	if len(parts) < 4 {
		return nil
	}
	traceID := parts[1]
	spanID := parts[2]
	flags := parts[3]
	if len(traceID) != 32 || len(spanID) != 16 {
		return nil
	}
	sampled := false
	if len(flags) >= 2 {
		flagByte, err := hex.DecodeString(flags[:2])
		if err == nil && len(flagByte) > 0 {
			sampled = (flagByte[0] & 0x01) != 0
		}
	}
	return &TraceEntry{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: sampled,
	}
}

// captureStack captures and formats parsed stack frames from the call site.
func captureStack(skip int) []StackFrame {
	const maxFrames = 50
	var frames []StackFrame
	pcs := make([]uintptr, maxFrames+skip)
	n := runtime.Callers(skip, pcs)
	if n == 0 {
		return frames
	}
	cf := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := cf.Next()
		if len(frames) >= maxFrames {
			break
		}
		inApp := isInApp(frame.Function, frame.File)
		frames = append(frames, StackFrame{
			Function: frame.Function,
			File:     frame.File,
			Line:     frame.Line,
			Column:   0,
			InApp:    inApp,
		})
		if !more {
			break
		}
	}
	return frames
}

func isInApp(function, file string) bool {
	if strings.Contains(function, "github.com/gothinkster/golang-gin-realworld-example-app") ||
		strings.Contains(file, "golang-gin-realworld-example-app") {
		return true
	}
	if strings.Contains(file, "/go/src/") ||
		strings.Contains(file, "/pkg/mod/") ||
		strings.Contains(file, "go/pkg/mod") ||
		strings.Contains(function, "gin-gonic") ||
		strings.Contains(function, "runtime.") ||
		strings.Contains(function, "gorm.io") {
		return false
	}
	return true
}

// BuildErrorEntry constructs a fully-populated LogEntry for an error.
func BuildErrorEntry(err error, context map[string]interface{}) LogEntry {
	return BuildErrorEntryWithSkip(err, context, 4)
}

// BuildErrorEntryWithSkip constructs a LogEntry with a custom stack skip level.
func BuildErrorEntryWithSkip(err error, context map[string]interface{}, skip int) LogEntry {
	if err == nil {
		return LogEntry{}
	}

	service := os.Getenv("LOGCORE_SERVICE")
	if service == "" {
		service = "golang-gin-realworld-example-app"
	}
	env := os.Getenv("LOGCORE_ENV")
	if env == "" {
		env = "dev"
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	msg := err.Error()
	if len(msg) > 65536 {
		msg = msg[:65536]
	}

	stack := captureStack(skip)
	insertID := computeInsertID(now, service, "ERROR", msg, context)

	return LogEntry{
		Timestamp: now,
		Severity:  "ERROR",
		Message:   msg,
		Service:   service,
		Env:       env,
		InsertID:  insertID,
		Error: &ErrorEntry{
			Type:    fmt.Sprintf("%T", err),
			Message: msg,
			Stack:   stack,
		},
		Context: context,
	}
}

// Emit sends a log entry using the default client.
func Emit(entry LogEntry) {
	DefaultClient().Emit(entry)
}

// CaptureError captures and emits an error event.
func CaptureError(err error, context map[string]interface{}) {
	if err == nil {
		return
	}
	entry := BuildErrorEntryWithSkip(err, context, 4)
	Emit(entry)
}

// Flush flushes the default client's queue.
func Flush() {
	DefaultClient().Flush()
}

// Close closes the default client.
func Close() {
	DefaultClient().Close()
}

// Middleware returns a Gin middleware for Logcore error capture and tracing using the default client.
func Middleware() gin.HandlerFunc {
	return MiddlewareWithClient(DefaultClient())
}

// MiddlewareWithClient returns a Gin middleware using the provided client.
func MiddlewareWithClient(client *Client) gin.HandlerFunc {
	if client == nil || !client.IsEnabled() {
		return func(c *gin.Context) {
			c.Next()
		}
	}

	return func(c *gin.Context) {
		trace := parseTraceparent(c.GetHeader("traceparent"))
		if trace != nil {
			c.Set("logcore_trace", trace)
		}

		defer func() {
			if r := recover(); r != nil {
				stack := captureStack(3)
				errMsg := fmt.Sprintf("%v", r)
				errType := fmt.Sprintf("%T", r)
				if rErr, ok := r.(error); ok {
					errMsg = rErr.Error()
				}

				ctx := map[string]interface{}{
					"method":     c.Request.Method,
					"path":       c.Request.URL.Path,
					"query":      c.Request.URL.RawQuery,
					"client_ip":  c.ClientIP(),
					"user_agent": c.Request.UserAgent(),
				}

				client.Emit(LogEntry{
					Severity: "ERROR",
					Message:  fmt.Sprintf("Panic recovered: %s", errMsg),
					Trace:    trace,
					Error: &ErrorEntry{
						Type:    errType,
						Message: errMsg,
						Stack:   stack,
					},
					Context: ctx,
				})

				panic(r)
			}
		}()

		c.Next()

		if len(c.Errors) > 0 {
			for _, ginErr := range c.Errors {
				stack := captureStack(3)
				errType := "Error"
				if ginErr.Err != nil {
					errType = fmt.Sprintf("%T", ginErr.Err)
				}
				ctx := map[string]interface{}{
					"method":     c.Request.Method,
					"path":       c.Request.URL.Path,
					"status":     c.Writer.Status(),
					"client_ip":  c.ClientIP(),
					"user_agent": c.Request.UserAgent(),
				}
				client.Emit(LogEntry{
					Severity: "ERROR",
					Message:  ginErr.Error(),
					Trace:    trace,
					Error: &ErrorEntry{
						Type:    errType,
						Message: ginErr.Error(),
						Stack:   stack,
					},
					Context: ctx,
				})
			}
		} else if c.Writer.Status() >= 500 {
			ctx := map[string]interface{}{
				"method":     c.Request.Method,
				"path":       c.Request.URL.Path,
				"status":     c.Writer.Status(),
				"client_ip":  c.ClientIP(),
				"user_agent": c.Request.UserAgent(),
			}
			client.Emit(LogEntry{
				Severity: "ERROR",
				Message:  fmt.Sprintf("HTTP %d error for %s %s", c.Writer.Status(), c.Request.Method, c.Request.URL.Path),
				Trace:    trace,
				Context:  ctx,
			})
		}
	}
}
