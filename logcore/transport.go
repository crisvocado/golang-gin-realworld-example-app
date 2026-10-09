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
	"time"
)

const (
	defaultBufferCap            = 1000
	defaultBatchSize            = 20
	defaultMaxEntriesPerRequest = 200
	defaultFlushInterval        = 5 * time.Second
	maxConsecutiveFailures      = 10
	maxMessageLength            = 65536
	maxLabelCount               = 32
	maxLabelValueLength         = 1024
	maxErrorTypeLength          = 256
	maxErrorMessageLength       = 65536
	maxStackFrames              = 50
)

// Transport manages buffering, batching, and asynchronous delivery of logs to Logcore.
type Transport struct {
	cfg Config

	buffer []*LogEntry
	mu     sync.Mutex

	flushChan chan struct{}
	closeChan chan struct{}
	doneChan  chan struct{}

	captureHook func(payload []byte)
	sender      func(req *http.Request) (*http.Response, error)
	httpClient  *http.Client

	consecutiveFailures int
	bufferCap           int
	batchSize           int
	maxEntriesPerReq    int
	flushInterval       time.Duration
}

// NewTransport creates and starts a new Logcore transport.
func NewTransport(cfg Config) *Transport {
	t := &Transport{
		cfg:              cfg,
		buffer:           make([]*LogEntry, 0, defaultBatchSize),
		flushChan:        make(chan struct{}, 1),
		closeChan:        make(chan struct{}),
		doneChan:         make(chan struct{}),
		httpClient:       &http.Client{Timeout: 10 * time.Second},
		bufferCap:        defaultBufferCap,
		batchSize:        defaultBatchSize,
		maxEntriesPerReq: defaultMaxEntriesPerRequest,
		flushInterval:    defaultFlushInterval,
	}

	go t.worker()
	return t
}

// SetCaptureHook sets a hook called with the serialized JSON payload prior to transmission.
func (t *Transport) SetCaptureHook(hook func(payload []byte)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.captureHook = hook
}

// SetSender sets a custom HTTP sender for testing.
func (t *Transport) SetSender(sender func(req *http.Request) (*http.Response, error)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sender = sender
}

// Enqueue adds a log entry to the buffer for delivery.
func (t *Transport) Enqueue(entry *LogEntry) {
	if entry == nil {
		return
	}

	if !t.cfg.Enabled {
		return
	}

	if !t.cfg.IsSeverityAllowed(entry.Severity) {
		return
	}

	// Apply defaults and constraints
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}

	if entry.Service == "" {
		entry.Service = t.cfg.Service
	}

	if entry.Env == "" {
		entry.Env = t.cfg.Env
	}

	if len(entry.Message) > maxMessageLength {
		entry.Message = entry.Message[:maxMessageLength]
	}

	if len(entry.Labels) > maxLabelCount {
		clampedLabels := make(map[string]string, maxLabelCount)
		count := 0
		for k, v := range entry.Labels {
			if count >= maxLabelCount {
				break
			}
			if len(v) > maxLabelValueLength {
				v = v[:maxLabelValueLength]
			}
			clampedLabels[k] = v
			count++
		}
		entry.Labels = clampedLabels
	} else if len(entry.Labels) > 0 {
		for k, v := range entry.Labels {
			if len(v) > maxLabelValueLength {
				entry.Labels[k] = v[:maxLabelValueLength]
			}
		}
	}

	if entry.Error != nil {
		if len(entry.Error.Type) > maxErrorTypeLength {
			entry.Error.Type = entry.Error.Type[:maxErrorTypeLength]
		}
		if len(entry.Error.Message) > maxErrorMessageLength {
			entry.Error.Message = entry.Error.Message[:maxErrorMessageLength]
		}
		if len(entry.Error.Stack) > maxStackFrames {
			entry.Error.Stack = entry.Error.Stack[:maxStackFrames]
		}
	}

	if entry.InsertID == "" {
		entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Circuit breaker check
	if t.consecutiveFailures >= maxConsecutiveFailures {
		// Buffer is kept empty while circuit is broken
		t.buffer = t.buffer[:0]
		return
	}

	// Drop oldest if buffer is at capacity
	if len(t.buffer) >= t.bufferCap {
		t.buffer = t.buffer[1:]
	}

	t.buffer = append(t.buffer, entry)

	if len(t.buffer) >= t.batchSize {
		select {
		case t.flushChan <- struct{}{}:
		default:
		}
	}
}

// computeInsertID calculates a deterministic 32-character hex insert ID.
func computeInsertID(timestamp, service, severity, message string, context map[string]interface{}) string {
	h := sha256.New()
	h.Write([]byte(timestamp))
	h.Write([]byte("|"))
	h.Write([]byte(service))
	h.Write([]byte("|"))
	h.Write([]byte(severity))
	h.Write([]byte("|"))
	h.Write([]byte(message))
	h.Write([]byte("|"))
	if len(context) > 0 {
		contextBytes, err := json.Marshal(context)
		if err == nil {
			h.Write(contextBytes)
		}
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:32]
}

func (t *Transport) worker() {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			t.flushBatch()
		case <-t.flushChan:
			t.flushBatch()
		case <-t.closeChan:
			t.flushAll()
			close(t.doneChan)
			return
		}
	}
}

// Flush forces an immediate synchronous flush of all buffered entries.
func (t *Transport) Flush() {
	t.flushAll()
}

// Close gracefully flushes all remaining entries and terminates the transport worker.
func (t *Transport) Close() {
	select {
	case <-t.closeChan:
		return
	default:
		close(t.closeChan)
		<-t.doneChan
	}
}

func (t *Transport) flushBatch() {
	for {
		entries := t.drain(t.maxEntriesPerReq)
		if len(entries) == 0 {
			break
		}
		t.sendEntries(entries)
	}
}

func (t *Transport) flushAll() {
	for {
		entries := t.drain(t.maxEntriesPerReq)
		if len(entries) == 0 {
			break
		}
		t.sendEntries(entries)
	}
}

func (t *Transport) drain(maxCount int) []*LogEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.buffer) == 0 {
		return nil
	}

	count := len(t.buffer)
	if count > maxCount {
		count = maxCount
	}

	batch := make([]*LogEntry, count)
	copy(batch, t.buffer[:count])
	t.buffer = t.buffer[count:]

	return batch
}

func (t *Transport) sendEntries(entries []*LogEntry) {
	if len(entries) == 0 {
		return
	}

	envelope := RequestEnvelope{
		SchemaVersion: 1,
		Entries:       entries,
	}

	payload, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcore: failed to marshal log entries: %v\n", err)
		return
	}

	t.mu.Lock()
	hook := t.captureHook
	sender := t.sender
	apiKey := t.cfg.Key
	endpoint := strings.TrimRight(t.cfg.URL, "/") + "/v1/logs"
	t.mu.Unlock()

	if hook != nil {
		hook(payload)
	}

	// If no API key and no mock sender, cannot deliver over network
	if apiKey == "" && sender == nil {
		return
	}

	// Retry loop: initial attempt + up to 3 retries (4 total attempts)
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<attempt*250) * time.Millisecond
			time.Sleep(backoff)
		}

		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(payload))
		if err != nil {
			fmt.Fprintf(os.Stderr, "logcore: failed to create request: %v\n", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", apiKey)

		var resp *http.Response
		if sender != nil {
			resp, err = sender(req)
		} else {
			resp, err = t.httpClient.Do(req)
		}

		if err != nil {
			// Network error -> retry
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = resp.Body.Close()
			t.mu.Lock()
			t.consecutiveFailures = 0
			t.mu.Unlock()
			return
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			// Transient server error or rate limiting -> retry
			_ = resp.Body.Close()
			continue
		}

		// Any other 4xx is permanent rejection -> do NOT retry
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		fmt.Fprintf(os.Stderr, "logcore: HTTP %d permanent rejection: %s\n", resp.StatusCode, string(bodyBytes))
		return
	}

	// Retries exhausted
	t.mu.Lock()
	t.consecutiveFailures++
	if t.consecutiveFailures >= maxConsecutiveFailures {
		t.buffer = t.buffer[:0]
		fmt.Fprintf(os.Stderr, "logcore: circuit breaker tripped after %d consecutive failures; buffer cleared\n", t.consecutiveFailures)
	}
	t.mu.Unlock()
}
