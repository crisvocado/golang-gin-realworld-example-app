package logcore_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gothinkster/golang-gin-realworld-example-app/logcore"
	"github.com/stretchr/testify/assert"
)

var (
	insertIDRegex = regexp.MustCompile(`^[0-9a-f]{32}$`)
	serviceRegex  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	traceIDRegex  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	spanIDRegex   = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func TestLogcoreEmit(t *testing.T) {
	gin.SetMode(gin.TestMode)

	client := logcore.GetClient()

	var capturedEnvelope *logcore.RequestEnvelope
	var rawCaptured []byte

	client.Transport().SetCaptureCallback(func(envelope *logcore.RequestEnvelope, rawJSON []byte) {
		capturedEnvelope = envelope
		rawCaptured = rawJSON
	})

	r := gin.New()
	r.Use(logcore.Middleware())

	r.GET("/test-error", func(c *gin.Context) {
		_ = c.Error(errors.New("test failure for logcore verification"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
	})

	req, err := http.NewRequest(http.MethodGet, "/test-error", nil)
	assert.NoError(t, err)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	client.Flush()

	assert.NotNil(t, capturedEnvelope, "Expected an envelope to be captured")
	assert.NotEmpty(t, rawCaptured, "Expected raw JSON payload to be captured")

	// Print the complete serialized JSON envelope to stdout for verification
	fmt.Println(string(rawCaptured))

	// Verify schema requirements
	assert.Equal(t, 1, capturedEnvelope.SchemaVersion)
	assert.NotEmpty(t, capturedEnvelope.Entries)

	entry := capturedEnvelope.Entries[0]
	assert.NotEmpty(t, entry.Timestamp)
	assert.Equal(t, "ERROR", entry.Severity)
	assert.NotEmpty(t, entry.Message)
	assert.Regexp(t, serviceRegex, entry.Service)
	assert.Contains(t, []string{"prod", "staging", "dev", "test", "local"}, entry.Env)
	assert.Regexp(t, insertIDRegex, entry.InsertID)

	// Verify trace if present
	if entry.Trace != nil {
		assert.Regexp(t, traceIDRegex, entry.Trace.TraceID)
		if entry.Trace.SpanID != "" {
			assert.Regexp(t, spanIDRegex, entry.Trace.SpanID)
		}
	}

	// Verify error object if present
	if entry.Error != nil {
		assert.NotEmpty(t, entry.Error.Type)
		assert.NotEmpty(t, entry.Error.Message)
		for _, frame := range entry.Error.Stack {
			assert.NotEmpty(t, frame.Function)
		}
	}

	// Ensure JSON is valid and round-trips
	var parsed logcore.RequestEnvelope
	err = json.Unmarshal(rawCaptured, &parsed)
	assert.NoError(t, err)
	assert.Equal(t, 1, parsed.SchemaVersion)
}
