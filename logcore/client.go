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
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var severityLevels = map[string]int{
	"DEBUG":    1,
	"INFO":     2,
	"WARNING":  3,
	"WARN":     3,
	"ERROR":    4,
	"CRITICAL": 5,
	"FATAL":    5,
}

func severityLevel(sev string) int {
	if lvl, ok := severityLevels[strings.ToUpper(sev)]; ok {
		return lvl
	}
	return 4 // Default to ERROR level
}

func isSeverityAllowed(sev, minSev string) bool {
	return severityLevel(sev) >= severityLevel(minSev)
}

// Client manages batching, buffering, and HTTP transport to the logcore gateway.
type Client struct {
	config              Config
	transportURL        string
	bufferChan          chan Entry
	httpClient          *http.Client
	stopChan            chan struct{}
	doneChan            chan struct{}
	consecutiveFailures int64
	circuitBroken       int32 // 0 = closed (healthy), 1 = open (tripped)
	closeOnce           sync.Once
	flushMu             sync.Mutex
}

// NewClient initializes a logcore HTTP transport client with background worker.
func NewClient(cfg Config) *Client {
	if cfg.Service == "" {
		cfg.Service = "golang-gin-realworld-example-app"
	}
	if cfg.Env == "" {
		cfg.Env = "dev"
	}
	if cfg.MinSeverity == "" {
		cfg.MinSeverity = "ERROR"
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 20
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}
	if cfg.MaxBuffer <= 0 {
		cfg.MaxBuffer = 1000
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	} else if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 10 * time.Second
	}

	url := strings.TrimRight(cfg.URL, "/")
	if url == "" {
		url = "https://logcore-dev-352942961463.us-east4.run.app"
	}
	if !strings.HasSuffix(url, "/v1/logs") {
		url = url + "/v1/logs"
	}

	c := &Client{
		config:       cfg,
		transportURL: url,
		bufferChan:   make(chan Entry, cfg.MaxBuffer),
		httpClient:   &http.Client{Timeout: cfg.HTTPTimeout},
		stopChan:     make(chan struct{}),
		doneChan:     make(chan struct{}),
	}

	go c.worker()
	return c
}

// IngestURL returns the target endpoint URL.
func (c *Client) IngestURL() string {
	return c.transportURL
}

// IsIngestURL checks if a target URL matches the logcore ingest endpoint to avoid self-logging loops.
func (c *Client) IsIngestURL(targetURL string) bool {
	if targetURL == "" || c.transportURL == "" {
		return false
	}
	return strings.Contains(targetURL, c.transportURL) || strings.Contains(c.transportURL, targetURL)
}

// NewEntry constructs a validated Entry with a deterministic insert_id and applied field clamps.
func (c *Client) NewEntry(severity, message string, errInfo *ErrorInfo, trace *TraceInfo, context map[string]any) Entry {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sev := strings.ToUpper(severity)
	if sev == "" {
		sev = "ERROR"
	}

	// Clamp message to 65536 characters
	if len(message) > 65536 {
		message = message[:65536]
	}

	// Clamp stack frames to 50
	if errInfo != nil && len(errInfo.Stack) > 50 {
		errInfo.Stack = errInfo.Stack[:50]
	}

	// Extract and clamp labels if present in context
	var labels map[string]string
	if context != nil {
		if rawLabels, ok := context["labels"].(map[string]string); ok {
			labels = make(map[string]string)
			count := 0
			for k, v := range rawLabels {
				if count >= 32 {
					break
				}
				if len(k) > 1024 {
					k = k[:1024]
				}
				if len(v) > 1024 {
					v = v[:1024]
				}
				labels[k] = v
				count++
			}
		}
	}

	// Canonical JSON of context for deterministic insert_id
	canonicalCtx := "{}"
	if len(context) > 0 {
		if data, err := json.Marshal(context); err == nil {
			canonicalCtx = string(data)
		}
	}

	// insert_id is derived: sha256(timestamp | service | severity | message | canonical_json(context))[:32]
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", now, c.config.Service, sev, message, canonicalCtx)
	h := sha256.Sum256([]byte(raw))
	insertID := hex.EncodeToString(h[:])[:32]

	return Entry{
		Timestamp: now,
		Severity:  sev,
		Message:   message,
		Service:   c.config.Service,
		Env:       c.config.Env,
		InsertID:  insertID,
		Trace:     trace,
		Error:     errInfo,
		Labels:    labels,
		Context:   context,
	}
}

// Enqueue puts an entry onto the internal buffer. It never blocks; when full, it drops the oldest entry.
func (c *Client) Enqueue(entry Entry) {
	if !c.config.Enabled || atomic.LoadInt32(&c.circuitBroken) == 1 {
		return
	}

	if !isSeverityAllowed(entry.Severity, c.config.MinSeverity) {
		return
	}

	select {
	case c.bufferChan <- entry:
		return
	default:
		// Buffer is full (1000 items): drop oldest item to maintain cap
		select {
		case <-c.bufferChan:
		default:
		}
		select {
		case c.bufferChan <- entry:
		default:
		}
	}
}

func (c *Client) worker() {
	defer close(c.doneChan)
	ticker := time.NewTicker(c.config.FlushInterval)
	defer ticker.Stop()

	var batch []Entry

	for {
		select {
		case entry, ok := <-c.bufferChan:
			if !ok {
				if len(batch) > 0 {
					c.flushBatch(batch)
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= c.config.BatchSize {
				c.flushBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				c.flushBatch(batch)
				batch = batch[:0]
			}
		case <-c.stopChan:
			// Drain remaining buffer on stop
			for {
				select {
				case entry := <-c.bufferChan:
					batch = append(batch, entry)
					if len(batch) >= 200 {
						c.flushBatch(batch)
						batch = batch[:0]
					}
				default:
					if len(batch) > 0 {
						c.flushBatch(batch)
						batch = batch[:0]
					}
					return
				}
			}
		}
	}
}

func (c *Client) flushBatch(entries []Entry) {
	if len(entries) == 0 || atomic.LoadInt32(&c.circuitBroken) == 1 {
		return
	}

	c.flushMu.Lock()
	defer c.flushMu.Unlock()

	// Split into chunks of at most 200 entries
	for i := 0; i < len(entries); i += 200 {
		end := i + 200
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[i:end]
		c.sendChunkWithRetry(chunk)
	}
}

func (c *Client) sendChunkWithRetry(chunk []Entry) {
	envelope := Envelope{
		SchemaVersion: 1,
		Entries:       chunk,
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcore: json marshal failed: %v\n", err)
		return
	}

	maxRetries := c.config.MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.transportURL, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "logcore: request creation failed: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if c.config.Key != "" {
			req.Header.Set("x-api-key", c.config.Key)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < maxRetries {
				backoff := time.Duration(1<<attempt) * 250 * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			c.recordFailure("network error: " + err.Error())
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
				backoff := time.Duration(1<<attempt) * 250 * time.Millisecond
				time.Sleep(backoff)
				continue
			}
			c.recordFailure(fmt.Sprintf("server error status %d: %s", resp.StatusCode, string(respBody)))
			return
		}

		// Permanent 4xx error (not 429): do NOT retry, echo server reason to stderr
		fmt.Fprintf(os.Stderr, "logcore: permanent rejection (%d): %s\n", resp.StatusCode, string(respBody))
		return
	}
}

func (c *Client) recordSuccess() {
	atomic.StoreInt64(&c.consecutiveFailures, 0)
	atomic.StoreInt32(&c.circuitBroken, 0)
}

func (c *Client) recordFailure(reason string) {
	fails := atomic.AddInt64(&c.consecutiveFailures, 1)
	if fails >= 10 {
		if atomic.CompareAndSwapInt32(&c.circuitBroken, 0, 1) {
			fmt.Fprintf(os.Stderr, "logcore: circuit breaker opened after %d consecutive failures: %s\n", fails, reason)
			// Drain buffer when circuit breaker opens
			for {
				select {
				case <-c.bufferChan:
				default:
					return
				}
			}
		}
	}
}

// Flush synchronously drains and sends all buffered entries.
func (c *Client) Flush() {
	c.flushMu.Lock()
	defer c.flushMu.Unlock()

	var batch []Entry
	for {
		select {
		case entry := <-c.bufferChan:
			batch = append(batch, entry)
			if len(batch) >= 200 {
				c.sendChunkWithRetry(batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				c.sendChunkWithRetry(batch)
			}
			return
		}
	}
}

// Close gracefully stops the worker goroutine.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.stopChan)
		<-c.doneChan
	})
}
