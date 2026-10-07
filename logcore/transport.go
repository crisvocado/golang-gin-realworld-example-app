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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var traceparentRegex = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// ParseTraceparent parses a W3C traceparent header.
func ParseTraceparent(header string) *TraceContext {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	matches := traceparentRegex.FindStringSubmatch(header)
	if len(matches) != 4 {
		return nil
	}
	traceID := matches[1]
	spanID := matches[2]
	flags := matches[3]

	if traceID == "00000000000000000000000000000000" || spanID == "0000000000000000" {
		return nil
	}

	return &TraceContext{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: flags == "01",
	}
}

// computeInsertID calculates a deterministic 32-character hex ID for deduplication.
func computeInsertID(timestamp, service, severity, message string, contextMap map[string]any) string {
	var ctxJSON string
	if len(contextMap) > 0 {
		if b, err := json.Marshal(contextMap); err == nil {
			ctxJSON = string(b)
		}
	}
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", timestamp, service, severity, message, ctxJSON)
	hash := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(hash[:])[:32]
}

// captureStack captures up to 50 call stack frames innermost first.
func captureStack(skip int) []StackFrame {
	const maxFrames = 50
	var frames []StackFrame
	pcs := make([]uintptr, maxFrames+skip)
	n := runtime.Callers(skip, pcs)
	if n == 0 {
		return nil
	}
	callersFrames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := callersFrames.Next()
		inApp := isAppFrame(frame.File)
		fn := frame.Function
		if len(fn) > 256 {
			fn = fn[:256]
		}
		file := frame.File
		if len(file) > 1024 {
			file = file[:1024]
		}
		frames = append(frames, StackFrame{
			Function: fn,
			File:     file,
			Line:     frame.Line,
			InApp:    inApp,
		})
		if !more || len(frames) >= maxFrames {
			break
		}
	}
	return frames
}

func isAppFrame(file string) bool {
	if file == "" {
		return false
	}
	if strings.Contains(file, "/runtime/") || strings.Contains(file, "/go/src/") {
		return false
	}
	if strings.Contains(file, "/pkg/mod/") {
		return false
	}
	if strings.Contains(file, "github.com/gin-gonic/") {
		return false
	}
	return true
}

func clampString(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

func clampEntry(entry *LogEntry) {
	entry.Message = clampString(entry.Message, 65536)
	if entry.Error != nil {
		entry.Error.Type = clampString(entry.Error.Type, 256)
		entry.Error.Message = clampString(entry.Error.Message, 65536)
		if len(entry.Error.Stack) > 50 {
			entry.Error.Stack = entry.Error.Stack[:50]
		}
		for i := range entry.Error.Stack {
			entry.Error.Stack[i].Function = clampString(entry.Error.Stack[i].Function, 256)
			entry.Error.Stack[i].File = clampString(entry.Error.Stack[i].File, 1024)
		}
	}
	if len(entry.Labels) > 32 {
		clampedLabels := make(map[string]string)
		count := 0
		for k, v := range entry.Labels {
			if count >= 32 {
				break
			}
			clampedLabels[clampString(k, 64)] = clampString(v, 1024)
			count++
		}
		entry.Labels = clampedLabels
	} else if len(entry.Labels) > 0 {
		for k, v := range entry.Labels {
			if len(v) > 1024 {
				entry.Labels[k] = clampString(v, 1024)
			}
		}
	}
}

type TransportSender func(req *http.Request) (*http.Response, error)
type CaptureCallback func(envelope *RequestEnvelope, rawJSON []byte)

type HTTPTransport struct {
	config          Config
	client          *http.Client
	customSender    TransportSender
	captureCallback CaptureCallback

	mu             sync.Mutex
	buffer         []LogEntry
	maxBufferSize  int
	batchSize      int
	flushInterval  time.Duration
	consecFailures int

	entryChan chan LogEntry
	flushChan chan chan struct{}
	stopChan  chan struct{}
	doneChan  chan struct{}
	closed    atomic.Bool
}

func NewHTTPTransport(cfg Config) *HTTPTransport {
	t := &HTTPTransport{
		config:        cfg,
		client:        &http.Client{Timeout: 10 * time.Second},
		maxBufferSize: 1000,
		batchSize:     20,
		flushInterval: 5 * time.Second,
		entryChan:     make(chan LogEntry, 1000),
		flushChan:     make(chan chan struct{}),
		stopChan:      make(chan struct{}),
		doneChan:      make(chan struct{}),
	}
	go t.workerLoop()
	return t
}

func (t *HTTPTransport) IngestURL() string {
	return t.config.URL + "/v1/logs"
}

func (t *HTTPTransport) SetSender(sender TransportSender) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.customSender = sender
}

func (t *HTTPTransport) SetCaptureCallback(cb CaptureCallback) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.captureCallback = cb
}

func (t *HTTPTransport) Enqueue(entry LogEntry) {
	if t.closed.Load() {
		return
	}
	select {
	case t.entryChan <- entry:
	default:
		select {
		case <-t.entryChan:
		default:
		}
		select {
		case t.entryChan <- entry:
		default:
		}
	}
}

func (t *HTTPTransport) Flush() {
	if t.closed.Load() {
		return
	}
	reply := make(chan struct{})
	select {
	case t.flushChan <- reply:
		<-reply
	case <-time.After(5 * time.Second):
	}
}

func (t *HTTPTransport) Close() {
	if t.closed.CompareAndSwap(false, true) {
		close(t.stopChan)
		<-t.doneChan
	}
}

func (t *HTTPTransport) workerLoop() {
	ticker := time.NewTicker(t.flushInterval)
	defer ticker.Stop()
	defer close(t.doneChan)

	for {
		select {
		case <-t.stopChan:
			for {
				select {
				case entry := <-t.entryChan:
					t.addToBuffer(entry)
				default:
					t.flushBuffer()
					return
				}
			}
		case reply := <-t.flushChan:
			for {
				select {
				case entry := <-t.entryChan:
					t.addToBuffer(entry)
				default:
					goto DRAINED
				}
			}
		DRAINED:
			t.flushBuffer()
			close(reply)
		case entry := <-t.entryChan:
			t.addToBuffer(entry)
			if len(t.buffer) >= t.batchSize {
				t.flushBuffer()
			}
		case <-ticker.C:
			if len(t.buffer) > 0 {
				t.flushBuffer()
			}
		}
	}
}

func (t *HTTPTransport) addToBuffer(entry LogEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.buffer) >= t.maxBufferSize {
		dropCount := len(t.buffer) - t.maxBufferSize + 1
		t.buffer = t.buffer[dropCount:]
	}
	t.buffer = append(t.buffer, entry)
}

func (t *HTTPTransport) flushBuffer() {
	t.mu.Lock()
	if len(t.buffer) == 0 {
		t.mu.Unlock()
		return
	}

	if t.consecFailures >= 10 {
		t.buffer = nil
		t.mu.Unlock()
		return
	}

	sendCount := len(t.buffer)
	if sendCount > 200 {
		sendCount = 200
	}
	batch := make([]LogEntry, sendCount)
	copy(batch, t.buffer[:sendCount])
	t.buffer = t.buffer[sendCount:]

	sender := t.customSender
	captureCb := t.captureCallback
	t.mu.Unlock()

	envelope := RequestEnvelope{
		SchemaVersion: 1,
		Entries:       batch,
	}

	bodyBytes, err := json.Marshal(envelope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logcore: failed to marshal log envelope: %v\n", err)
		return
	}

	if captureCb != nil {
		captureCb(&envelope, bodyBytes)
	}

	if t.config.Key == "" && sender == nil {
		return
	}

	success := t.sendWithRetry(bodyBytes, sender)

	t.mu.Lock()
	if success {
		t.consecFailures = 0
	} else {
		t.consecFailures++
		if t.consecFailures >= 10 {
			t.buffer = nil
		}
	}
	t.mu.Unlock()
}

func (t *HTTPTransport) sendWithRetry(bodyBytes []byte, sender TransportSender) bool {
	endpoint := t.IngestURL()
	maxAttempts := 4

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(math.Pow(2, float64(attempt-1))*250) * time.Millisecond
			time.Sleep(backoff)
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			fmt.Fprintf(os.Stderr, "logcore: failed to create request: %v\n", err)
			return false
		}
		req.Header.Set("Content-Type", "application/json")
		if t.config.Key != "" {
			req.Header.Set("x-api-key", t.config.Key)
		}

		var resp *http.Response
		if sender != nil {
			resp, err = sender(req)
		} else {
			resp, err = t.client.Do(req)
		}

		if err != nil {
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return true
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			continue
		}

		fmt.Fprintf(os.Stderr, "logcore: permanent rejection %d: %s\n", resp.StatusCode, string(respBody))
		return false
	}

	return false
}
