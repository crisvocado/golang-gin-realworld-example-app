package logcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type customMockPoster struct {
	doFunc func(req *http.Request) (*http.Response, error)
}

func (c *customMockPoster) Do(req *http.Request) (*http.Response, error) {
	if c.doFunc != nil {
		return c.doFunc(req)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"ok"}`))),
		Header:     make(http.Header),
	}, nil
}

func TestInsertIDGeneration(t *testing.T) {
	ctx := map[string]interface{}{"route": "/test", "status": 500}
	id1 := ComputeInsertID("2026-08-17T18:30:00.000Z", "app", "ERROR", "test message", ctx)
	id2 := ComputeInsertID("2026-08-17T18:30:00.000Z", "app", "ERROR", "test message", ctx)

	assert.Equal(t, id1, id2)
	assert.Len(t, id1, 32)
	assert.Regexp(t, "^[0-9a-f]{32}$", id1)
}

func TestSeverityFiltering(t *testing.T) {
	var postedCount int32
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			atomic.AddInt32(&postedCount, 1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	// Info should be dropped
	client.Enqueue(Entry{Severity: "INFO", Message: "info msg"})
	// Warn should be dropped
	client.Enqueue(Entry{Severity: "WARN", Message: "warn msg"})
	client.Flush()

	assert.Equal(t, int32(0), atomic.LoadInt32(&postedCount))

	// Error should be enqueued and sent
	client.Enqueue(Entry{Severity: "ERROR", Message: "error msg"})
	client.Flush()

	assert.Equal(t, int32(1), atomic.LoadInt32(&postedCount))
}

func TestClamping(t *testing.T) {
	var capturedEnvelope RequestEnvelope
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(b, &capturedEnvelope)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	longMessage := strings.Repeat("x", 70000)
	labels := make(map[string]string)
	for i := 0; i < 40; i++ {
		labels[fmt.Sprintf("k%d", i)] = strings.Repeat("v", 1200)
	}

	frames := make([]StackFrame, 60)
	for i := 0; i < 60; i++ {
		frames[i] = StackFrame{Function: fmt.Sprintf("fn%d", i), File: "test.go", Line: i, InApp: true}
	}

	client.Enqueue(Entry{
		Severity: "ERROR",
		Message:  longMessage,
		Labels:   labels,
		Error: &ErrorInfo{
			Type:    "TestError",
			Message: "err",
			Stack:   frames,
		},
	})
	client.Flush()

	assert.Len(t, capturedEnvelope.Entries, 1)
	entry := capturedEnvelope.Entries[0]
	assert.Equal(t, 65536, len(entry.Message))
	assert.LessOrEqual(t, len(entry.Labels), 32)
	for _, v := range entry.Labels {
		assert.LessOrEqual(t, len(v), 1024)
	}
	assert.Equal(t, 50, len(entry.Error.Stack))
}

func TestRetryOn500AndRecovery(t *testing.T) {
	var attempts int32
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			att := atomic.AddInt32(&attempts, 1)
			if att < 3 {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"server error"}`))),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"ok"}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	client.Enqueue(Entry{Severity: "ERROR", Message: "retry test"})
	client.Flush()

	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))
}

func TestPermanentRejection400(t *testing.T) {
	var attempts int32
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			atomic.AddInt32(&attempts, 1)
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"bad request"}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	client.Enqueue(Entry{Severity: "ERROR", Message: "reject test"})
	client.Flush()

	assert.Equal(t, int32(1), atomic.LoadInt32(&attempts), "400 should not be retried")
}

func TestCircuitBreaker(t *testing.T) {
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"persistent 500"}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	for i := 0; i < 12; i++ {
		client.Enqueue(Entry{Severity: "ERROR", Message: fmt.Sprintf("msg %d", i)})
		client.Flush()
	}

	client.mu.Lock()
	assert.GreaterOrEqual(t, client.consecutiveFailures, circuitBreakerMax)
	client.mu.Unlock()
}

func TestBufferOverflowDropsOldest(t *testing.T) {
	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	// Stop worker from sending by clearing URL or using no poster
	client.config.URL = ""
	defer client.Close()

	for i := 0; i < maxBufferSize+50; i++ {
		client.Enqueue(Entry{
			Severity: "ERROR",
			Message:  fmt.Sprintf("msg-%d", i),
		})
	}

	client.mu.Lock()
	assert.Equal(t, maxBufferSize, len(client.queue))
	assert.Equal(t, "msg-50", client.queue[0].Message)
	client.mu.Unlock()
}

func TestTraceExtraction(t *testing.T) {
	// 1. W3C traceparent
	req, _ := http.NewRequest("GET", "/", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	tr := ExtractTrace(req)
	assert.NotNil(t, tr)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", tr.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", tr.SpanID)
	assert.True(t, tr.Sampled)

	// 2. X-Cloud-Trace-Context
	req2, _ := http.NewRequest("GET", "/", nil)
	req2.Header.Set("X-Cloud-Trace-Context", "105445aa7843bc8bf206b120001000/1;o=1")
	tr2 := ExtractTrace(req2)
	assert.NotNil(t, tr2)
	assert.Equal(t, "105445aa7843bc8bf206b120001000", tr2.TraceID)
	assert.Equal(t, "1", tr2.SpanID)
	assert.True(t, tr2.Sampled)

	// 3. X-Request-ID
	req3, _ := http.NewRequest("GET", "/", nil)
	req3.Header.Set("X-Request-ID", "c28c86d8-79b7-4a0b-bc1c-6d9b9c9f0b1a")
	tr3 := ExtractTrace(req3)
	assert.NotNil(t, tr3)
	assert.Equal(t, "c28c86d879b74a0bbc1c6d9b9c9f0b1a", tr3.TraceID)

	// 4. GenerateTrace
	trGen := GenerateTrace()
	assert.NotNil(t, trGen)
	assert.Len(t, trGen.TraceID, 32)
	assert.Len(t, trGen.SpanID, 16)
}

func TestMiddlewarePanicHandling(t *testing.T) {
	var capturedEnvelope RequestEnvelope
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(b, &capturedEnvelope)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(MiddlewareWithClient(client))

	r.GET("/panic", func(c *gin.Context) {
		panic("database connection lost")
	})

	req, _ := http.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	client.Flush()

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Len(t, capturedEnvelope.Entries, 1)
	entry := capturedEnvelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Contains(t, entry.Message, "database connection lost")
	assert.NotNil(t, entry.Error)
	assert.NotEmpty(t, entry.Error.Stack)
}

func TestLogErrorExplicit(t *testing.T) {
	var capturedEnvelope RequestEnvelope
	mockPoster := &customMockPoster{
		doFunc: func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(b, &capturedEnvelope)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
			}, nil
		},
	}

	client := NewClient(Config{
		URL:         "https://example.com",
		Key:         "key",
		Enabled:     true,
		MinSeverity: "ERROR",
	})
	client.SetPoster(mockPoster)
	defer client.Close()

	testErr := errors.New("sql query failed")
	client.LogError(testErr, "failed to query users", map[string]string{"table": "users"}, map[string]interface{}{"retry": 1})
	client.Flush()

	assert.Len(t, capturedEnvelope.Entries, 1)
	entry := capturedEnvelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, "failed to query users", entry.Message)
	assert.Equal(t, "users", entry.Labels["table"])
	assert.NotNil(t, entry.Error)
	assert.Equal(t, "sql query failed", entry.Error.Message)
}

func TestIngestURLSelfExclusion(t *testing.T) {
	client := NewClient(Config{
		URL: "https://logcore.example.com/",
	})
	assert.Equal(t, "https://logcore.example.com/v1/logs", client.IngestURL())
}

func TestEnvironmentNormalization(t *testing.T) {
	assert.Equal(t, "prod", normalizeEnv("production"))
	assert.Equal(t, "prod", normalizeEnv("PROD"))
	assert.Equal(t, "staging", normalizeEnv("STAGING"))
	assert.Equal(t, "test", normalizeEnv("testing"))
	assert.Equal(t, "local", normalizeEnv("LOCAL"))
	assert.Equal(t, "dev", normalizeEnv("development"))
	assert.Equal(t, "dev", normalizeEnv(""))
}
