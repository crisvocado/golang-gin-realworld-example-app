package logcore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestTraceparentParsingAndGeneration(t *testing.T) {
	asserts := assert.New(t)

	// Valid traceparent
	header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	trace := ParseTraceparent(header)
	asserts.NotNil(trace)
	asserts.Equal("4bf92f3577b34da6a3ce929d0e0e4736", trace.TraceID)
	asserts.Equal("00f067aa0ba902b7", trace.SpanID)
	asserts.True(trace.Sampled)
	asserts.Equal(header, trace.Traceparent())

	// Unsampled traceparent
	unsampledHeader := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	unsampledTrace := ParseTraceparent(unsampledHeader)
	asserts.NotNil(unsampledTrace)
	asserts.False(unsampledTrace.Sampled)

	// Invalid traceparent
	asserts.Nil(ParseTraceparent(""))
	asserts.Nil(ParseTraceparent("invalid-traceparent"))
	asserts.Nil(ParseTraceparent("00-00000000000000000000000000000000-0000000000000000-01"))

	// Generated trace
	generated := GenerateTrace()
	asserts.NotNil(generated)
	asserts.Len(generated.TraceID, 32)
	asserts.Len(generated.SpanID, 16)
	asserts.True(generated.Sampled)
	asserts.Regexp(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, generated.Traceparent())
}

func TestDeterministicInsertID(t *testing.T) {
	asserts := assert.New(t)

	ts := "2026-08-17T18:30:00.000Z"
	svc := "golang-gin-realworld-example-app"
	sev := "ERROR"
	msg := "test error message"
	ctx := map[string]interface{}{"key": "value"}

	id1 := ComputeInsertID(ts, svc, sev, msg, ctx)
	id2 := ComputeInsertID(ts, svc, sev, msg, ctx)

	asserts.Equal(id1, id2, "Deterministic insert_id must produce identical hashes for identical inputs")
	asserts.Len(id1, 32, "insert_id must be exactly 32 hex characters")
	asserts.Regexp(regexp.MustCompile(`^[0-9a-f]{32}$`), id1)

	// Different message produces different ID
	id3 := ComputeInsertID(ts, svc, sev, "different message", ctx)
	asserts.NotEqual(id1, id3)
}

func TestStackCaptureAndInApp(t *testing.T) {
	asserts := assert.New(t)

	frames := CaptureStack(1, 10)
	asserts.NotEmpty(frames)
	asserts.True(len(frames) <= 10)

	// Check fields
	firstFrame := frames[0]
	asserts.NotEmpty(firstFrame.Function)
	asserts.NotEmpty(firstFrame.File)
	asserts.True(firstFrame.Line > 0)

	// Check isInApp helper
	asserts.False(isInApp("/usr/local/go/src/runtime/panic.go", "runtime.gopanic"))
	asserts.False(isInApp("/go/pkg/mod/github.com/gin-gonic/gin@v1.10.0/context.go", "gin.Context.Next"))
	asserts.False(isInApp("/workspace/golang-gin-realworld-example-app/logcore/stack.go", "logcore.CaptureStack"))
	asserts.True(isInApp("/workspace/golang-gin-realworld-example-app/users/routers.go", "users.UserRetrieve"))
}

func TestEnvelopeAndEntrySerialization(t *testing.T) {
	asserts := assert.New(t)

	entry := &Entry{
		Timestamp: "2026-08-17T18:30:00.000Z",
		Severity:  "ERROR",
		Message:   "checkout failed",
		Service:   "golang-gin-realworld-example-app",
		Env:       "dev",
		InsertID:  "7c2e1a0b9d8f6e4c2a0b8d6f4e2c0a8b",
		Trace: &Trace{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
			SpanID:  "00f067aa0ba902b7",
			Sampled: true,
		},
		Error: &ErrorInfo{
			Type:    "CustomError",
			Message: "test failure",
			Stack: []Frame{
				{
					Function: "testFunc",
					File:     "test.go",
					Line:     42,
					Column:   0,
					InApp:    true,
				},
			},
		},
		Labels: map[string]string{
			"env": "dev",
		},
		Context: map[string]interface{}{
			"http": map[string]interface{}{
				"status": 500,
			},
		},
	}

	envelope := Envelope{
		SchemaVersion: 1,
		Entries:       []*Entry{entry},
	}

	data, err := json.Marshal(envelope)
	asserts.NoError(err)

	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	asserts.NoError(err)

	asserts.Equal(float64(1), parsed["schema_version"])
	entries, ok := parsed["entries"].([]interface{})
	asserts.True(ok)
	asserts.Len(entries, 1)

	first := entries[0].(map[string]interface{})
	asserts.Equal("2026-08-17T18:30:00.000Z", first["timestamp"])
	asserts.Equal("ERROR", first["severity"])
	asserts.Equal("checkout failed", first["message"])
	asserts.Equal("golang-gin-realworld-example-app", first["service"])
	asserts.Equal("dev", first["env"])
	asserts.Equal("7c2e1a0b9d8f6e4c2a0b8d6f4e2c0a8b", first["insert_id"])

	traceObj := first["trace"].(map[string]interface{})
	asserts.Equal("4bf92f3577b34da6a3ce929d0e0e4736", traceObj["traceId"])
	asserts.Equal("00f067aa0ba902b7", traceObj["spanId"])
	asserts.Equal(true, traceObj["sampled"])

	errObj := first["error"].(map[string]interface{})
	asserts.Equal("CustomError", errObj["type"])
	asserts.Equal("test failure", errObj["message"])
	stackList := errObj["stack"].([]interface{})
	asserts.Len(stackList, 1)
	stackFrame := stackList[0].(map[string]interface{})
	asserts.Equal("testFunc", stackFrame["function"])
	asserts.Equal("test.go", stackFrame["file"])
	asserts.Equal(float64(42), stackFrame["line"])
	asserts.Equal(true, stackFrame["inApp"])
	// Crucial check: verify that inApp is present and NOT in_app
	asserts.NotContains(stackFrame, "in_app")
}

func TestHTTPTransportDelivery(t *testing.T) {
	asserts := assert.New(t)

	var receivedHeaders http.Header
	var receivedBody []byte
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		mu.Unlock()

		assert.Equal(t, "/v1/logs", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-api-key", r.Header.Get("x-api-key"))

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	cfg := Config{
		URL:         server.URL,
		Key:         "test-api-key",
		Enabled:     true,
		Service:     "test-service",
		Env:         "test",
		MinSeverity: SeverityError,
		HTTPClient:  server.Client(),
	}

	client := NewClient(cfg)
	defer client.Close()

	client.Error("test error message", errors.New("underlying error"))

	err := client.Flush(3 * time.Second)
	asserts.NoError(err)

	mu.Lock()
	defer mu.Unlock()

	asserts.NotEmpty(receivedHeaders.Get("x-api-key"))
	asserts.NotEmpty(receivedBody)
	var env Envelope
	err = json.Unmarshal(receivedBody, &env)
	asserts.NoError(err)
	asserts.Equal(1, env.SchemaVersion)
	asserts.Len(env.Entries, 1)
	asserts.Equal("test error message", env.Entries[0].Message)
	asserts.Equal("test-service", env.Entries[0].Service)
	asserts.Equal("test", env.Entries[0].Env)
	asserts.Equal("ERROR", env.Entries[0].Severity)
	asserts.NotEmpty(env.Entries[0].InsertID)
}

func TestGinMiddlewareIntegration(t *testing.T) {
	asserts := assert.New(t)

	var receivedEntries []*Entry
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env Envelope
		if err := json.Unmarshal(body, &env); err == nil {
			mu.Lock()
			receivedEntries = append(receivedEntries, env.Entries...)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		URL:         server.URL,
		Key:         "test-key",
		Enabled:     true,
		Service:     "test-app",
		Env:         "test",
		MinSeverity: SeverityError,
		HTTPClient:  server.Client(),
	}

	client := Init(
		WithURL(cfg.URL),
		WithKey(cfg.Key),
		WithService(cfg.Service),
		WithEnv(cfg.Env),
	)
	client.transport.client = server.Client()
	defer client.Close()

	r := gin.New()
	r.Use(Middleware())

	r.GET("/panic", func(c *gin.Context) {
		panic("intentional route panic")
	})

	r.GET("/server-error", func(c *gin.Context) {
		_ = c.Error(errors.New("custom route error"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	})

	r.GET("/success", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 1. Test success route (should NOT emit any error)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/success", nil)
	r.ServeHTTP(w, req)
	asserts.Equal(http.StatusOK, w.Code)
	asserts.NotEmpty(w.Header().Get("traceparent"))

	// 2. Test server error route (should emit ERROR)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/server-error", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	r.ServeHTTP(w, req)
	asserts.Equal(http.StatusInternalServerError, w.Code)

	// 3. Test panic route (should recover, return 500, emit CRITICAL)
	w = httptest.NewRecorder()
	req, _ = http.NewRequest(http.MethodGet, "/panic", nil)
	r.ServeHTTP(w, req)
	asserts.Equal(http.StatusInternalServerError, w.Code)

	err := client.Flush(3 * time.Second)
	asserts.NoError(err)

	mu.Lock()
	defer mu.Unlock()

	asserts.Len(receivedEntries, 2)

	// First error was server-error
	asserts.Equal(SeverityError, receivedEntries[0].Severity)
	asserts.Equal("4bf92f3577b34da6a3ce929d0e0e4736", receivedEntries[0].Trace.TraceID)
	asserts.Equal("/server-error", receivedEntries[0].Context["http"].(map[string]interface{})["path"])

	// Second error was panic
	asserts.Equal(SeverityCritical, receivedEntries[1].Severity)
	asserts.Contains(receivedEntries[1].Message, "Panic in GET /panic")
	asserts.Equal("/panic", receivedEntries[1].Context["http"].(map[string]interface{})["path"])
}

func TestSeverityFloorFiltering(t *testing.T) {
	asserts := assert.New(t)

	var receivedEntries []*Entry
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env Envelope
		if err := json.Unmarshal(body, &env); err == nil {
			mu.Lock()
			receivedEntries = append(receivedEntries, env.Entries...)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{
		URL:         server.URL,
		Key:         "test-key",
		Enabled:     true,
		Service:     "test-app",
		Env:         "test",
		MinSeverity: SeverityError,
		HTTPClient:  server.Client(),
	}

	client := NewClient(cfg)
	defer client.Close()

	// 1. Info entry (below floor ERROR)
	client.LogEntry(&Entry{
		Severity: SeverityInfo,
		Message:  "info message",
	})

	// 2. Error entry (at floor ERROR)
	client.LogEntry(&Entry{
		Severity: SeverityError,
		Message:  "error message",
	})

	err := client.Flush(3 * time.Second)
	asserts.NoError(err)

	mu.Lock()
	defer mu.Unlock()

	asserts.Len(receivedEntries, 1)
	asserts.Equal("error message", receivedEntries[0].Message)
}

func TestClampingAndUnwrapping(t *testing.T) {
	asserts := assert.New(t)

	// Test Unwrapping
	baseErr := errors.New("root cause database timeout")
	wrappedErr := fmt.Errorf("service layer: %w", baseErr)
	root := UnwrapRootError(wrappedErr)
	asserts.Equal(baseErr, root)

	// Test Clamping in Transport
	cfg := Config{
		URL:         "http://localhost:12345",
		Enabled:     true,
		Service:     "test-app",
		Env:         "test",
		MinSeverity: SeverityError,
	}
	transport := &HTTPTransport{
		config: cfg,
	}

	largeMessage := string(make([]byte, 70000))
	largeLabels := make(map[string]string)
	for i := 0; i < 40; i++ {
		largeLabels[fmt.Sprintf("k%d", i)] = string(make([]byte, 2000))
	}
	largeStack := make([]Frame, 60)

	entry := &Entry{
		Message: largeMessage,
		Labels:  largeLabels,
		Error: &ErrorInfo{
			Stack: largeStack,
		},
	}

	transport.prepareEntry(entry)

	asserts.Equal(maxMessageLength, len(entry.Message))
	asserts.True(len(entry.Labels) <= maxLabelCount)
	for _, v := range entry.Labels {
		asserts.True(len(v) <= maxLabelLength)
	}
	asserts.Equal(maxFramesCount, len(entry.Error.Stack))
}

func TestBuildEntry(t *testing.T) {
	asserts := assert.New(t)

	client := NewClient(Config{
		Service: "test-svc",
		Env:     "dev",
	})
	defer client.Close()

	testErr := errors.New("underlying issue")
	trace := GenerateTrace()
	entry := client.BuildEntry("test message", testErr,
		WithTrace(trace),
		WithLabels(map[string]string{"foo": "bar"}),
		WithContext(map[string]interface{}{"extra": 123}),
	)

	asserts.NotNil(entry)
	asserts.Equal("test message", entry.Message)
	asserts.Equal("test-svc", entry.Service)
	asserts.Equal("dev", entry.Env)
	asserts.Equal("ERROR", entry.Severity)
	asserts.NotEmpty(entry.InsertID)
	asserts.Len(entry.InsertID, 32)
	asserts.Equal(trace.TraceID, entry.Trace.TraceID)
	asserts.Equal("underlying issue", entry.Error.Message)
	asserts.NotEmpty(entry.Error.Stack)
	asserts.Equal("bar", entry.Labels["foo"])
	asserts.Equal(123, entry.Context["extra"])
}
