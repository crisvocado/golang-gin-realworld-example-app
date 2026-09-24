package logcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

type mockHTTPPoster struct {
	lastRequest  *http.Request
	lastEnvelope *RequestEnvelope
}

func (m *mockHTTPPoster) Do(req *http.Request) (*http.Response, error) {
	m.lastRequest = req
	bodyBytes, _ := io.ReadAll(req.Body)
	var env RequestEnvelope
	if err := json.Unmarshal(bodyBytes, &env); err == nil {
		m.lastEnvelope = &env
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"status":"ok"}`))),
		Header:     make(http.Header),
	}, nil
}

func TestLogcoreEmit(t *testing.T) {
	mockPoster := &mockHTTPPoster{}
	cfg := Config{
		URL:         "https://logcore-dev-352942961463.us-east4.run.app",
		Key:         "test-key",
		Enabled:     true,
		Service:     "golang-gin-realworld-example-app",
		Env:         "dev",
		MinSeverity: "ERROR",
	}

	client := NewClient(cfg)
	client.SetPoster(mockPoster)
	defer client.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(MiddlewareWithClient(client))

	r.GET("/trigger-error", func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
	})

	req, _ := http.NewRequest(http.MethodGet, "/trigger-error", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	client.Flush()

	assert.NotNil(t, mockPoster.lastEnvelope, "envelope should have been posted")
	assert.Equal(t, 1, mockPoster.lastEnvelope.SchemaVersion)
	assert.NotEmpty(t, mockPoster.lastEnvelope.Entries)

	entry := mockPoster.lastEnvelope.Entries[0]
	assert.Equal(t, "ERROR", entry.Severity)
	assert.Equal(t, "golang-gin-realworld-example-app", entry.Service)
	assert.Equal(t, "dev", entry.Env)
	assert.NotEmpty(t, entry.InsertID)
	assert.Len(t, entry.InsertID, 32)
	assert.NotNil(t, entry.Trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry.Trace.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", entry.Trace.SpanID)
	assert.True(t, entry.Trace.Sampled)
	assert.NotNil(t, entry.Error)
	assert.NotEmpty(t, entry.Error.Stack)

	entryJSON, err := json.Marshal(entry)
	assert.NoError(t, err)
	fmt.Println(string(entryJSON))
}
