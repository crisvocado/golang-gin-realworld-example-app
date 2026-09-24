package logcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxBufferSize     = 1000
	maxBatchSize      = 200
	batchTriggerSize  = 20
	flushInterval     = 5 * time.Second
	maxRetries        = 3
	maxMessageLength  = 65536
	maxLabelCount     = 32
	maxLabelLength    = 1024
	circuitBreakerMax = 10
)

var (
	defaultClient     *Client
	defaultClientOnce sync.Once
)

// HTTPPoster interface allows stubbing HTTP calls in tests.
type HTTPPoster interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client manages batching, buffering, and shipping structured logs to Logcore.
type Client struct {
	config Config
	poster HTTPPoster

	mu                  sync.Mutex
	queue               []Entry
	consecutiveFailures int

	wakeChan  chan struct{}
	flushChan chan chan struct{}
	closeChan chan struct{}
	doneChan  chan struct{}

	closed bool
}

// LoadConfigFromEnv reads Logcore settings from environment variables.
func LoadConfigFromEnv() Config {
	url := strings.TrimSpace(os.Getenv("LOGCORE_URL"))
	key := strings.TrimSpace(os.Getenv("LOGCORE_KEY"))
	enabledStr := strings.TrimSpace(os.Getenv("LOGCORE_ENABLED"))
	service := strings.TrimSpace(os.Getenv("LOGCORE_SERVICE"))
	env := strings.TrimSpace(os.Getenv("LOGCORE_ENV"))
	minSev := strings.TrimSpace(os.Getenv("LOGCORE_MIN_SEVERITY"))

	if service == "" {
		service = "golang-gin-realworld-example-app"
	}
	env = normalizeEnv(env)

	if minSev == "" {
		minSev = "ERROR"
	}

	enabled := true
	if enabledStr != "" {
		lower := strings.ToLower(enabledStr)
		if lower == "false" || lower == "0" || lower == "no" || lower == "off" {
			enabled = false
		}
	} else if url == "" && key == "" {
		// If neither url nor key is provided, disable active shipping
		enabled = false
	}

	return Config{
		URL:         url,
		Key:         key,
		Enabled:     enabled,
		Service:     service,
		Env:         env,
		MinSeverity: minSev,
	}
}

func normalizeEnv(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "prod", "production":
		return "prod"
	case "staging", "stage":
		return "staging"
	case "test", "testing":
		return "test"
	case "local":
		return "local"
	case "dev", "development":
		return "dev"
	default:
		if raw != "" {
			return strings.ToLower(strings.TrimSpace(raw))
		}
		return "dev"
	}
}

// NewClient creates and starts a new Logcore Client.
func NewClient(cfg Config) *Client {
	c := &Client{
		config: cfg,
		poster: &http.Client{
			Timeout: 10 * time.Second,
		},
		queue:     make([]Entry, 0, batchTriggerSize),
		wakeChan:  make(chan struct{}, 1),
		flushChan: make(chan chan struct{}),
		closeChan: make(chan struct{}),
		doneChan:  make(chan struct{}),
	}

	go c.worker()
	return c
}

// SetPoster sets custom HTTP client/poster for testing.
func (c *Client) SetPoster(p HTTPPoster) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.poster = p
}

// IngestURL returns the configured ingestion endpoint URL for self-exclusion.
func (c *Client) IngestURL() string {
	if c.config.URL == "" {
		return ""
	}
	return strings.TrimRight(c.config.URL, "/") + "/v1/logs"
}

// GetDefaultClient returns the shared singleton Logcore client.
func GetDefaultClient() *Client {
	defaultClientOnce.Do(func() {
		defaultClient = NewClient(LoadConfigFromEnv())
	})
	return defaultClient
}

// Close gracefully stops the default client and flushes buffered logs.
func Close() {
	if defaultClient != nil {
		defaultClient.Close()
	}
}

// Flush flushes pending logs from default client.
func Flush() {
	if defaultClient != nil {
		defaultClient.Flush()
	}
}

// Close gracefully flushes and stops the client worker.
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.mu.Unlock()

	close(c.closeChan)
	<-c.doneChan
}

// Flush synchronously triggers a flush of all currently queued items.
func (c *Client) Flush() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	done := make(chan struct{})
	select {
	case c.flushChan <- done:
		<-done
	case <-c.doneChan:
	}
}

// Enqueue validates, clamps, and enqueues a log entry. Returns immediately.
func (c *Client) Enqueue(entry Entry) {
	if !c.config.Enabled {
		return
	}

	if !isSeverityAllowed(entry.Severity, c.config.MinSeverity) {
		return
	}

	// Apply defaults and clamps
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if entry.Service == "" {
		entry.Service = c.config.Service
	}
	if entry.Env == "" {
		entry.Env = c.config.Env
	}
	if len(entry.Message) > maxMessageLength {
		entry.Message = entry.Message[:maxMessageLength]
	}

	// Clamp labels
	if len(entry.Labels) > 0 {
		clampedLabels := make(map[string]string, len(entry.Labels))
		count := 0
		for k, v := range entry.Labels {
			if count >= maxLabelCount {
				break
			}
			if len(v) > maxLabelLength {
				v = v[:maxLabelLength]
			}
			clampedLabels[k] = v
			count++
		}
		entry.Labels = clampedLabels
	}

	// Clamp stack frames
	if entry.Error != nil && len(entry.Error.Stack) > maxStackFrames {
		entry.Error.Stack = entry.Error.Stack[:maxStackFrames]
	}

	// Compute deterministic insert_id
	if entry.InsertID == "" {
		entry.InsertID = ComputeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}

	// Buffer cap: drop oldest if full
	if len(c.queue) >= maxBufferSize {
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, entry)
	wake := len(c.queue) >= batchTriggerSize
	c.mu.Unlock()

	if wake {
		select {
		case c.wakeChan <- struct{}{}:
		default:
		}
	}
}

// ComputeInsertID generates a deterministic 32-hex character ID for deduplication.
func ComputeInsertID(timestamp, service, severity, message string, context map[string]interface{}) string {
	var ctxJSON string
	if len(context) > 0 {
		b, err := json.Marshal(context)
		if err == nil {
			ctxJSON = string(b)
		}
	}
	payload := fmt.Sprintf("%s|%s|%s|%s|%s", timestamp, service, severity, message, ctxJSON)
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])[:32]
}

func severityRank(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return 100
	case "INFO":
		return 200
	case "NOTICE":
		return 300
	case "WARN", "WARNING":
		return 400
	case "ERROR":
		return 500
	case "CRITICAL":
		return 600
	case "FATAL":
		return 700
	case "EMERGENCY":
		return 800
	default:
		return 500
	}
}

func isSeverityAllowed(severity, minSeverity string) bool {
	return severityRank(severity) >= severityRank(minSeverity)
}

func (c *Client) worker() {
	defer close(c.doneChan)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.wakeChan:
			c.flushBatches()
		case <-ticker.C:
			c.flushBatches()
		case done := <-c.flushChan:
			c.flushBatches()
			close(done)
		case <-c.closeChan:
			c.flushBatches()
			return
		}
	}
}

func (c *Client) flushBatches() {
	for {
		c.mu.Lock()
		if len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}

		batchSize := len(c.queue)
		if batchSize > maxBatchSize {
			batchSize = maxBatchSize
		}
		batch := make([]Entry, batchSize)
		copy(batch, c.queue[:batchSize])
		c.queue = c.queue[batchSize:]
		c.mu.Unlock()

		c.sendBatchWithRetry(batch)
	}
}

func (c *Client) sendBatchWithRetry(entries []Entry) {
	if len(entries) == 0 || c.config.URL == "" {
		return
	}

	c.mu.Lock()
	if c.consecutiveFailures >= circuitBreakerMax {
		// Circuit breaker tripped: drop batches and do not send
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	endpoint := strings.TrimRight(c.config.URL, "/") + "/v1/logs"
	envelope := RequestEnvelope{
		SchemaVersion: 1,
		Entries:       entries,
	}

	data, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcore: failed to marshal envelope: %v\n", err)
		return
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoffMs := int(math.Pow(2, float64(attempt-1))) * 250
			time.Sleep(time.Duration(backoffMs) * time.Millisecond)
		}

		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			fmt.Fprintf(os.Stderr, "logcore: failed to create request: %v\n", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		if c.config.Key != "" {
			req.Header.Set("x-api-key", c.config.Key)
		}

		resp, err := c.poster.Do(req)
		if err != nil {
			// Network error -> retry
			if attempt == maxRetries {
				c.recordFailure()
			}
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// Success
			c.recordSuccess()
			return
		}

		// Check if permanent rejection (4xx except 429)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			fmt.Fprintf(os.Stderr, "logcore: permanent rejection %d: %s\n", resp.StatusCode, string(respBody))
			// Drop batch, do not retry
			return
		}

		// 5xx or 429 -> retry
		if attempt == maxRetries {
			c.recordFailure()
		}
	}
}

func (c *Client) recordSuccess() {
	c.mu.Lock()
	c.consecutiveFailures = 0
	c.mu.Unlock()
}

func (c *Client) recordFailure() {
	c.mu.Lock()
	c.consecutiveFailures++
	if c.consecutiveFailures >= circuitBreakerMax {
		// Empty remaining buffer on circuit breaker trip
		c.queue = nil
		fmt.Fprintf(os.Stderr, "logcore: circuit breaker tripped after %d consecutive failures, buffer cleared\n", c.consecutiveFailures)
	}
	c.mu.Unlock()
}

// LogPanic logs a recovered panic as an ERROR entry.
func (c *Client) LogPanic(ctx *gin.Context, rec interface{}, trace *TraceContext) {
	errInfo := BuildErrorInfo(rec, 3)
	msg := fmt.Sprintf("panic: %v", rec)

	var reqContext map[string]interface{}
	var labels map[string]string

	if ctx != nil && ctx.Request != nil {
		reqContext = map[string]interface{}{
			"http": map[string]interface{}{
				"method": ctx.Request.Method,
				"url":    ctx.Request.URL.String(),
				"path":   ctx.Request.URL.Path,
				"host":   ctx.Request.Host,
			},
		}
		labels = map[string]string{
			"http_method": ctx.Request.Method,
			"http_path":   ctx.Request.URL.Path,
			"status":      "500",
		}
	}

	entry := Entry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  "ERROR",
		Message:   msg,
		Service:   c.config.Service,
		Env:       c.config.Env,
		Trace:     trace,
		Error:     errInfo,
		Labels:    labels,
		Context:   reqContext,
	}

	c.Enqueue(entry)
}

// LogGinError logs Gin context errors or 5xx status codes.
func (c *Client) LogGinError(ctx *gin.Context, trace *TraceContext) {
	if ctx == nil {
		return
	}

	var errMsg string
	var errInfo *ErrorInfo

	if len(ctx.Errors) > 0 {
		lastErr := ctx.Errors.Last().Err
		errMsg = ctx.Errors.String()
		errInfo = BuildErrorInfo(lastErr, 3)
	} else {
		status := ctx.Writer.Status()
		errMsg = fmt.Sprintf("HTTP %d %s %s", status, ctx.Request.Method, ctx.Request.URL.Path)
		errInfo = BuildErrorInfo(fmt.Errorf("HTTP request failed with status %d", status), 3)
	}

	var reqContext map[string]interface{}
	var labels map[string]string

	if ctx.Request != nil {
		reqContext = map[string]interface{}{
			"http": map[string]interface{}{
				"method": ctx.Request.Method,
				"url":    ctx.Request.URL.String(),
				"path":   ctx.Request.URL.Path,
				"host":   ctx.Request.Host,
			},
		}
		labels = map[string]string{
			"http_method": ctx.Request.Method,
			"http_path":   ctx.Request.URL.Path,
			"status":      fmt.Sprintf("%d", ctx.Writer.Status()),
		}
	}

	entry := Entry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  "ERROR",
		Message:   errMsg,
		Service:   c.config.Service,
		Env:       c.config.Env,
		Trace:     trace,
		Error:     errInfo,
		Labels:    labels,
		Context:   reqContext,
	}

	c.Enqueue(entry)
}

// LogError logs an explicit error and message.
func (c *Client) LogError(err error, message string, labels map[string]string, ctx map[string]interface{}) {
	if message == "" && err != nil {
		message = err.Error()
	}
	errInfo := BuildErrorInfo(err, 2)

	entry := Entry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  "ERROR",
		Message:   message,
		Service:   c.config.Service,
		Env:       c.config.Env,
		Error:     errInfo,
		Labels:    labels,
		Context:   ctx,
	}

	c.Enqueue(entry)
}
