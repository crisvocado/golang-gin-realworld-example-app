package logcore

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestParseTraceparent(t *testing.T) {
	// Valid traceparent
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tc := parseTraceparent(tp)
	assert.NotNil(t, tc)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tc.TraceID)
	assert.NotNil(t, tc.SpanID)
	assert.Equal(t, "00f067aa0ba902b7", *tc.SpanID)
	assert.True(t, tc.Sampled)

	// All zeros trace ID is invalid
	tpInvalid := "00-00000000000000000000000000000000-00f067aa0ba902b7-01"
	assert.Nil(t, parseTraceparent(tpInvalid))

	// Malformed traceparent
	assert.Nil(t, parseTraceparent("invalid"))
	assert.Nil(t, parseTraceparent(""))
}

func TestConfigDefaults(t *testing.T) {
	cfg := LoadConfigFromEnv()
	assert.NotEmpty(t, cfg.URL)
	assert.NotEmpty(t, cfg.Service)
	assert.NotEmpty(t, cfg.Env)
	assert.True(t, cfg.Enabled)
	assert.True(t, cfg.IsSeverityAllowed("ERROR"))
	assert.True(t, cfg.IsSeverityAllowed("CRITICAL"))
	assert.False(t, cfg.IsSeverityAllowed("INFO"))
}

func TestTransportCaptureAndSerialization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured []byte
	tCfg := Config{
		URL:         DefaultEndpoint,
		Key:         "test-key",
		Enabled:     true,
		Service:     "test-service",
		Env:         "dev",
		MinSeverity: "ERROR",
	}

	transport := NewTransport(tCfg)
	defer transport.Close()

	transport.SetCaptureHook(func(payload []byte) {
		captured = payload
	})

	// Stub sender
	transport.SetSender(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		}, nil
	})

	r := gin.New()
	r.Use(MiddlewareWithTransport(transport))

	r.GET("/test-err", func(c *gin.Context) {
		_ = c.Error(errors.New("test handler error"))
		c.JSON(500, gin.H{"error": "internal"})
	})

	req, _ := http.NewRequest("GET", "/test-err", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	transport.Flush()

	assert.NotEmpty(t, captured)

	var env RequestEnvelope
	err := json.Unmarshal(captured, &env)
	assert.NoError(t, err)
	assert.Equal(t, 1, env.SchemaVersion)
	assert.Len(t, env.Entries, 1)

	entry := env.Entries[0]
	assert.Equal(t, "CRITICAL", entry.Severity)
	assert.Equal(t, "test-service", entry.Service)
	assert.Equal(t, "dev", entry.Env)
	assert.Len(t, entry.InsertID, 32)
	assert.NotNil(t, entry.Trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry.Trace.TraceID)
	assert.NotNil(t, entry.Error)
	assert.Equal(t, "test handler error", entry.Error.Message)
	assert.NotEmpty(t, entry.Error.Stack)
	assert.True(t, entry.Error.Stack[0].InApp)
}

func TestDeterministicInsertID(t *testing.T) {
	ts := "2026-10-09T06:35:50.000Z"
	ctx := map[string]interface{}{"path": "/api/test"}
	id1 := computeInsertID(ts, "svc", "ERROR", "msg", ctx)
	id2 := computeInsertID(ts, "svc", "ERROR", "msg", ctx)
	assert.Equal(t, id1, id2)
	assert.Len(t, id1, 32)
}

func TestRetryAndCircuitBreaker(t *testing.T) {
	tCfg := Config{
		URL:         DefaultEndpoint,
		Key:         "test-key",
		Enabled:     true,
		Service:     "test-service",
		Env:         "dev",
		MinSeverity: "ERROR",
	}

	transport := NewTransport(tCfg)
	defer transport.Close()

	attempts := 0
	transport.SetSender(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader("server error")),
		}, nil
	})

	transport.Enqueue(&LogEntry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  "ERROR",
		Message:   "circuit breaker test",
	})

	transport.Flush()
	assert.Equal(t, 4, attempts) // 1 initial + 3 retries
}
