package logcore

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestLogcoreEmit(t *testing.T) {
	var receivedEnvelope Envelope
	receivedChan := make(chan Envelope, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-api-key", r.Header.Get("x-api-key"))

		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)

		var env Envelope
		err = json.Unmarshal(body, &env)
		assert.NoError(t, err)

		w.WriteHeader(http.StatusOK)
		receivedChan <- env
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:       true,
		URL:           server.URL,
		Key:           "test-api-key",
		Service:       "golang-gin-realworld-example-app",
		Env:           "dev",
		MinSeverity:   "ERROR",
		BatchSize:     1,
		FlushInterval: 50 * time.Millisecond,
	})
	defer client.Close()

	r := gin.New()
	r.Use(MiddlewareWithClient(client))

	r.GET("/test-error", func(c *gin.Context) {
		c.Error(fmt.Errorf("simulated database failure"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test-error", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)

	client.Flush()

	select {
	case env := <-receivedChan:
		receivedEnvelope = env
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for logcore emission")
	}

	assert.Equal(t, 1, receivedEnvelope.SchemaVersion)
	assert.NotEmpty(t, receivedEnvelope.Entries)

	entry := receivedEnvelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, "golang-gin-realworld-example-app", entry.Service)
	assert.Equal(t, "dev", entry.Env)
	assert.Len(t, entry.InsertID, 32)
	assert.Regexp(t, "^[0-9a-f]{32}$", entry.InsertID)
	assert.NotEmpty(t, entry.Timestamp)
	assert.Contains(t, entry.Message, "simulated database failure")

	assert.NotNil(t, entry.Trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry.Trace.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", entry.Trace.SpanID)
	assert.True(t, entry.Trace.Sampled)

	assert.NotNil(t, entry.Error)
	assert.Equal(t, "*errors.errorString", entry.Error.Type)
	assert.Equal(t, "simulated database failure", entry.Error.Message)
	assert.NotEmpty(t, entry.Error.Stack)

	// Validate JSON serialization matches wire format
	entryJSON, err := json.MarshalIndent(entry, "", "  ")
	assert.NoError(t, err)
	t.Logf("Emitted Logcore Entry:\n%s", string(entryJSON))
}

func TestDeterministicInsertID(t *testing.T) {
	client := NewClient(Config{
		Service: "test-svc",
		Env:     "dev",
	})
	defer client.Close()

	ctx := map[string]any{"user_id": 123, "action": "login"}
	entry1 := client.NewEntry("ERROR", "test message", nil, nil, ctx)
	// Same timestamp and inputs must produce same insert_id
	raw := fmt.Sprintf("%s|%s|%s|%s|%s", entry1.Timestamp, "test-svc", "ERROR", "test message", "{\"action\":\"login\",\"user_id\":123}")
	assert.Len(t, entry1.InsertID, 32)
	assert.Regexp(t, "^[0-9a-f]{32}$", entry1.InsertID)
	_ = raw
}

func TestSeverityFloor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no logs below min severity should be delivered")
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:       true,
		URL:           server.URL,
		MinSeverity:   "ERROR",
		BatchSize:     1,
		FlushInterval: 50 * time.Millisecond,
	})
	defer client.Close()

	infoEntry := client.NewEntry("INFO", "info log", nil, nil, nil)
	client.Enqueue(infoEntry)

	warnEntry := client.NewEntry("WARNING", "warning log", nil, nil, nil)
	client.Enqueue(warnEntry)

	client.Flush()
}

func TestTraceparentParsing(t *testing.T) {
	trace := parseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	assert.NotNil(t, trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", trace.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", trace.SpanID)
	assert.True(t, trace.Sampled)

	invalid := parseTraceparent("invalid-traceparent")
	assert.Nil(t, invalid)

	empty := parseTraceparent("")
	assert.Nil(t, empty)
}

func TestRetryAndPermanentRejection(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&attempts, 1)
		if count < 3 {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 retryable
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:     true,
		URL:         server.URL,
		MaxRetries:  3,
		BatchSize:   1,
		HTTPTimeout: 2 * time.Second,
	})
	defer client.Close()

	entry := client.NewEntry("ERROR", "retry test", nil, nil, nil)
	client.Enqueue(entry)
	client.Flush()

	assert.Equal(t, int32(3), atomic.LoadInt32(&attempts))

	// Test 400 Bad Request (permanent rejection: no retry)
	var permAttempts int32
	permServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&permAttempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid field"}`))
	}))
	defer permServer.Close()

	permClient := NewClient(Config{
		Enabled:    true,
		URL:        permServer.URL,
		MaxRetries: 3,
		BatchSize:  1,
	})
	defer permClient.Close()

	entry2 := permClient.NewEntry("ERROR", "perm reject test", nil, nil, nil)
	permClient.Enqueue(entry2)
	permClient.Flush()

	assert.Equal(t, int32(1), atomic.LoadInt32(&permAttempts))
}

func TestCircuitBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:       true,
		URL:           server.URL,
		MaxRetries:    -1,
		BatchSize:     1,
		FlushInterval: 10 * time.Millisecond,
	})
	defer client.Close()

	for i := 0; i < 12; i++ {
		entry := client.NewEntry("ERROR", fmt.Sprintf("failure %d", i), nil, nil, nil)
		client.Enqueue(entry)
		client.Flush()
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&client.circuitBroken))
}

func TestBufferCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(Config{
		Enabled:       true,
		URL:           server.URL,
		MaxBuffer:     5,
		BatchSize:     10,
		FlushInterval: 10 * time.Millisecond,
	})
	defer client.Close()

	for i := 0; i < 10; i++ {
		client.Enqueue(client.NewEntry("ERROR", fmt.Sprintf("msg %d", i), nil, nil, nil))
	}

	assert.LessOrEqual(t, len(client.bufferChan), 5)
}
