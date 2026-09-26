package logcore

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestComputeInsertID(t *testing.T) {
	id1 := computeInsertID("2026-08-17T18:30:00.000Z", "my-service", "ERROR", "something broke", map[string]interface{}{"foo": "bar"})
	id2 := computeInsertID("2026-08-17T18:30:00.000Z", "my-service", "ERROR", "something broke", map[string]interface{}{"foo": "bar"})
	id3 := computeInsertID("2026-08-17T18:30:00.000Z", "my-service", "ERROR", "different error", map[string]interface{}{"foo": "bar"})

	assert.Equal(t, 32, len(id1), "insert_id must be exactly 32 hex chars")
	assert.Regexp(t, regexp.MustCompile("^[0-9a-f]{32}$"), id1)
	assert.Equal(t, id1, id2, "insert_id must be deterministic")
	assert.NotEqual(t, id1, id3, "insert_id must differ for different messages")
}

func TestParseTraceparent(t *testing.T) {
	// Valid traceparent
	header := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	trace := parseTraceparent(header)
	assert.NotNil(t, trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", trace.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", trace.SpanID)
	assert.True(t, trace.Sampled)

	// Unsampled traceparent
	headerUnsampled := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	trace2 := parseTraceparent(headerUnsampled)
	assert.NotNil(t, trace2)
	assert.False(t, trace2.Sampled)

	// Invalid traceparents
	assert.Nil(t, parseTraceparent(""))
	assert.Nil(t, parseTraceparent("invalid"))
	assert.Nil(t, parseTraceparent("00-short-short-01"))
}

func TestCaptureStack(t *testing.T) {
	frames := captureStack(1)
	assert.NotEmpty(t, frames)
	assert.True(t, frames[0].InApp, "Caller in current package should be inApp=true")
	assert.NotEmpty(t, frames[0].Function)
	assert.NotEmpty(t, frames[0].File)
}

func TestClientSendBatch(t *testing.T) {
	var receivedHeaders http.Header
	var receivedBody []byte

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		URL:         server.URL,
		Key:         "test-api-key",
		Enabled:     true,
		Service:     "test-service",
		Env:         "test",
		MinSeverity: "ERROR",
		HTTPClient:  server.Client(),
	})
	defer client.Close()

	client.Emit(LogEntry{
		Severity: "ERROR",
		Message:  "test error message",
		Trace: &TraceEntry{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
			SpanID:  "00f067aa0ba902b7",
			Sampled: true,
		},
		Error: &ErrorEntry{
			Type:    "TestError",
			Message: "test error message",
			Stack: []StackFrame{
				{
					Function: "TestClientSendBatch",
					File:     "logcore_test.go",
					Line:     42,
					Column:   0,
					InApp:    true,
				},
			},
		},
		Context: map[string]interface{}{
			"foo": "bar",
		},
	})

	client.Flush()

	assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
	assert.Equal(t, "test-api-key", receivedHeaders.Get("x-api-key"))

	var envelope Envelope
	err := json.Unmarshal(receivedBody, &envelope)
	assert.NoError(t, err)
	assert.Equal(t, 1, envelope.SchemaVersion)
	assert.Len(t, envelope.Entries, 1)

	entry := envelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, "test error message", entry.Message)
	assert.Equal(t, "test-service", entry.Service)
	assert.Equal(t, "test", entry.Env)
	assert.Regexp(t, regexp.MustCompile("^[0-9a-f]{32}$"), entry.InsertID)
	assert.NotNil(t, entry.Trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry.Trace.TraceID)
	assert.NotNil(t, entry.Error)
	assert.Equal(t, "TestError", entry.Error.Type)
	assert.Len(t, entry.Error.Stack, 1)
	assert.True(t, entry.Error.Stack[0].InApp)
}

func TestGinMiddlewarePanicRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{
		URL:         server.URL,
		Key:         "test-key",
		Enabled:     true,
		Service:     "test-app",
		Env:         "test",
		HTTPClient:  server.Client(),
	})
	defer client.Close()

	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(MiddlewareWithClient(client))
	router.GET("/panic", func(c *gin.Context) {
		panic("test panic occurred")
	})

	req, _ := http.NewRequest("GET", "/panic", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	client.Flush()

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var envelope Envelope
	err := json.Unmarshal(receivedBody, &envelope)
	assert.NoError(t, err)
	assert.Len(t, envelope.Entries, 1)
	entry := envelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Contains(t, entry.Message, "Panic recovered: test panic occurred")
	assert.NotNil(t, entry.Trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry.Trace.TraceID)
	assert.NotNil(t, entry.Error)
	assert.Equal(t, "test panic occurred", entry.Error.Message)
	assert.NotEmpty(t, entry.Error.Stack)
}

func TestBuildErrorEntry(t *testing.T) {
	err := assert.AnError
	entry := BuildErrorEntry(err, map[string]interface{}{"component": "test"})

	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, err.Error(), entry.Message)
	assert.Equal(t, "golang-gin-realworld-example-app", entry.Service)
	assert.Equal(t, "dev", entry.Env)
	assert.Regexp(t, regexp.MustCompile("^[0-9a-f]{32}$"), entry.InsertID)
	assert.NotNil(t, entry.Error)
	assert.Equal(t, err.Error(), entry.Error.Message)
	assert.NotEmpty(t, entry.Error.Stack)
	assert.True(t, entry.Error.Stack[0].InApp)
	assert.Equal(t, "test", entry.Context["component"])
}
