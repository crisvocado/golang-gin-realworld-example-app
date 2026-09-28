package logcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	maxBufferSize      = 1000
	batchTriggerSize   = 20
	maxBatchSize       = 200
	flushInterval      = 5 * time.Second
	maxRetries         = 3
	maxConsecutiveFail = 10
	maxMessageLength   = 65536
	maxLabelCount      = 32
	maxLabelLength     = 1024
	maxFramesCount     = 50
)

// HTTPTransport implements the Logcore HTTP delivery transport with batching, retries, and circuit breaker.
type HTTPTransport struct {
	config        Config
	endpoint      string
	apiKey        string
	client        *http.Client
	entriesChan   chan *Entry
	flushChan     chan chan struct{}
	closeOnce     sync.Once
	closed        chan struct{}
	wg            sync.WaitGroup
	failStreak    int32
	isCircuitOpen int32
}

// NewHTTPTransport creates a new background worker transport.
func NewHTTPTransport(cfg Config) *HTTPTransport {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	endpoint := cfg.URL
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	endpoint = fmt.Sprintf("%s/v1/logs", stringsTrimRight(endpoint, "/"))

	t := &HTTPTransport{
		config:      cfg,
		endpoint:    endpoint,
		apiKey:      cfg.Key,
		client:      client,
		entriesChan: make(chan *Entry, maxBufferSize),
		flushChan:   make(chan chan struct{}),
		closed:      make(chan struct{}),
	}

	t.wg.Add(1)
	go t.worker()

	return t
}

func stringsTrimRight(s, cutset string) string {
	for len(s) > 0 && bytes.ContainsAny([]byte{s[len(s)-1]}, cutset) {
		s = s[:len(s)-1]
	}
	return s
}

// Send queues an entry for background delivery without blocking the caller.
func (t *HTTPTransport) Send(entry *Entry) {
	if entry == nil || !t.config.Enabled {
		return
	}

	// Check severity floor
	entryRank := severityRanks[entry.Severity]
	floorRank := severityRanks[t.config.MinSeverity]
	if entryRank < floorRank {
		return
	}

	// Sanitize and clamp entry
	t.prepareEntry(entry)

	// If circuit breaker is open, discard entry
	if atomic.LoadInt32(&t.isCircuitOpen) == 1 {
		return
	}

	// Non-blocking channel push; drop oldest if full
	select {
	case t.entriesChan <- entry:
	default:
		// Buffer full: drop oldest
		select {
		case <-t.entriesChan:
		default:
		}
		select {
		case t.entriesChan <- entry:
		default:
		}
	}
}

// prepareEntry populates defaults, clamps strings/arrays, and computes the deterministic insert_id.
func (t *HTTPTransport) prepareEntry(entry *Entry) {
	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if entry.Severity == "" {
		entry.Severity = SeverityError
	}
	if entry.Service == "" {
		entry.Service = t.config.Service
	}
	if entry.Env == "" {
		entry.Env = t.config.Env
	}

	// Clamp message length
	if len(entry.Message) > maxMessageLength {
		entry.Message = entry.Message[:maxMessageLength]
	}

	// Clamp labels
	if len(entry.Labels) > 0 {
		clampedLabels := make(map[string]string, maxLabelCount)
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
	if entry.Error != nil && len(entry.Error.Stack) > maxFramesCount {
		entry.Error.Stack = entry.Error.Stack[:maxFramesCount]
	}

	// Compute deterministic insert_id: sha256(timestamp | service | severity | message | canonical_json(context))[:32]
	if entry.InsertID == "" {
		entry.InsertID = ComputeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	}
}

// ComputeInsertID computes a deterministic 32-hex character ID for an entry.
func ComputeInsertID(timestamp, service, severity, message string, context map[string]interface{}) string {
	var ctxStr string
	if len(context) > 0 {
		if b, err := json.Marshal(context); err == nil {
			ctxStr = string(b)
		}
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", timestamp, service, severity, message, ctxStr)
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])[:32]
}

// worker manages batching, periodic flushing, and dispatching entries.
func (t *HTTPTransport) worker() {
	defer t.wg.Done()

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	var batch []*Entry

	flushBatch := func() {
		if len(batch) == 0 {
			return
		}
		entriesToSend := batch
		batch = nil
		t.sendBatchWithRetry(entriesToSend)
	}

	for {
		select {
		case <-t.closed:
			// Drain remaining entries
			for {
				select {
				case entry := <-t.entriesChan:
					batch = append(batch, entry)
					if len(batch) >= maxBatchSize {
						flushBatch()
					}
				default:
					flushBatch()
					return
				}
			}

		case done := <-t.flushChan:
			// Drain all pending entries in channel before flushing
			for {
				select {
				case entry := <-t.entriesChan:
					batch = append(batch, entry)
				default:
					goto drained
				}
			}
		drained:
			flushBatch()
			close(done)

		case entry := <-t.entriesChan:
			batch = append(batch, entry)
			if len(batch) >= batchTriggerSize {
				flushBatch()
			}

		case <-ticker.C:
			flushBatch()
		}
	}
}

// sendBatchWithRetry handles sending an envelope with exponential backoff and circuit breaking.
func (t *HTTPTransport) sendBatchWithRetry(entries []*Entry) {
	if len(entries) == 0 {
		return
	}

	// Split into sub-batches if exceeding maxBatchSize
	for len(entries) > 0 {
		chunkSize := len(entries)
		if chunkSize > maxBatchSize {
			chunkSize = maxBatchSize
		}
		chunk := entries[:chunkSize]
		entries = entries[chunkSize:]

		t.dispatchChunk(chunk)
	}
}

func (t *HTTPTransport) dispatchChunk(entries []*Entry) {
	envelope := Envelope{
		SchemaVersion: 1,
		Entries:       entries,
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[logcore] failed to marshal envelope: %v\n", err)
		return
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequest(http.MethodPost, t.endpoint, bytes.NewReader(body))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[logcore] failed to create http request: %v\n", err)
			return
		}

		req.Header.Set("Content-Type", "application/json")
		if t.apiKey != "" {
			req.Header.Set("x-api-key", t.apiKey)
		}

		resp, err := t.client.Do(req)
		if err != nil {
			// Network error -> retry with backoff
			t.onTransientFailure(attempt)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		statusCode := resp.StatusCode
		if statusCode >= 200 && statusCode < 300 {
			// Success -> reset circuit breaker streak
			atomic.StoreInt32(&t.failStreak, 0)
			atomic.StoreInt32(&t.isCircuitOpen, 0)
			return
		}

		if statusCode == 429 || statusCode >= 500 {
			// 429 or 5xx -> transient server failure -> retry with backoff
			t.onTransientFailure(attempt)
			continue
		}

		// Permanent 4xx failure (e.g. 400, 401, 403, 422) -> do NOT retry, echo reason to stderr
		fmt.Fprintf(os.Stderr, "[logcore] HTTP %d permanent rejection from %s: %s\n", statusCode, t.endpoint, string(respBody))
		t.recordFailure()
		return
	}

	// Exhausted all retries
	t.recordFailure()
}

func (t *HTTPTransport) onTransientFailure(attempt int) {
	if attempt < maxRetries {
		backoff := time.Duration(math.Pow(2, float64(attempt))*250) * time.Millisecond
		time.Sleep(backoff)
	}
}

func (t *HTTPTransport) recordFailure() {
	streak := atomic.AddInt32(&t.failStreak, 1)
	if streak >= maxConsecutiveFail {
		// Trip circuit breaker
		if atomic.CompareAndSwapInt32(&t.isCircuitOpen, 0, 1) {
			fmt.Fprintf(os.Stderr, "[logcore] circuit breaker tripped after %d consecutive flush failures; discarding buffered entries\n", streak)
			// Drain buffered channel
			for {
				select {
				case <-t.entriesChan:
				default:
					return
				}
			}
		}
	}
}

// Flush synchronously flushes any queued entries within the timeout.
func (t *HTTPTransport) Flush(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan struct{})
	select {
	case t.flushChan <- done:
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close flushes queued entries and shuts down the worker goroutine.
func (t *HTTPTransport) Close() error {
	var err error
	t.closeOnce.Do(func() {
		close(t.closed)
		t.wg.Wait()
	})
	return err
}
