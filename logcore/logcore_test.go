package logcore_test

import (
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gothinkster/golang-gin-realworld-example-app/logcore"
	"github.com/stretchr/testify/assert"
)

func TestParseTraceparent(t *testing.T) {
	assert.Nil(t, logcore.ParseTraceparent(""))
	assert.Nil(t, logcore.ParseTraceparent("invalid"))
	assert.Nil(t, logcore.ParseTraceparent("00-00000000000000000000000000000000-0000000000000000-01"))

	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	trace := logcore.ParseTraceparent(valid)
	assert.NotNil(t, trace)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", trace.TraceID)
	assert.Equal(t, "00f067aa0ba902b7", trace.SpanID)
	assert.True(t, trace.Sampled)

	notSampled := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
	trace2 := logcore.ParseTraceparent(notSampled)
	assert.NotNil(t, trace2)
	assert.False(t, trace2.Sampled)
}

func TestConfigLoading(t *testing.T) {
	os.Setenv("LOGCORE_URL", "https://custom.endpoint.com/")
	os.Setenv("LOGCORE_SERVICE", "my-service")
	os.Setenv("LOGCORE_ENV", "prod")
	os.Setenv("LOGCORE_MIN_SEVERITY", "WARNING")
	defer func() {
		os.Unsetenv("LOGCORE_URL")
		os.Unsetenv("LOGCORE_SERVICE")
		os.Unsetenv("LOGCORE_ENV")
		os.Unsetenv("LOGCORE_MIN_SEVERITY")
	}()

	cfg := logcore.LoadConfig()
	assert.Equal(t, "https://custom.endpoint.com", cfg.URL)
	assert.Equal(t, "my-service", cfg.Service)
	assert.Equal(t, "prod", cfg.Env)
	assert.Equal(t, "WARNING", cfg.MinSeverity)
}

func TestHTTPTransportBatchingAndRetry(t *testing.T) {
	cfg := logcore.Config{
		URL:         "https://example.com",
		Key:         "dummy-key",
		Enabled:     true,
		Service:     "test-service",
		Env:         "test",
		MinSeverity: "ERROR",
	}

	transport := logcore.NewHTTPTransport(cfg)
	defer transport.Close()

	var capturedCount int
	transport.SetCaptureCallback(func(envelope *logcore.RequestEnvelope, rawJSON []byte) {
		capturedCount += len(envelope.Entries)
	})

	attempts := 0
	transport.SetSender(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts < 2 {
			// Simulate 500 error on first attempt
			return &http.Response{
				StatusCode: 500,
				Body:       http.NoBody,
			}, nil
		}
		// Success on second attempt
		return &http.Response{
			StatusCode: 200,
			Body:       http.NoBody,
		}, nil
	})

	transport.Enqueue(logcore.LogEntry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Severity:  "ERROR",
		Message:   "batch test",
		Service:   "test-service",
		Env:       "test",
		InsertID:  "0123456789abcdef0123456789abcdef",
	})

	transport.Flush()

	assert.Equal(t, 1, capturedCount)
	assert.Equal(t, 2, attempts)
}

func TestClientLogError(t *testing.T) {
	client := logcore.GetClient()

	var lastEnvelope *logcore.RequestEnvelope
	client.Transport().SetCaptureCallback(func(envelope *logcore.RequestEnvelope, rawJSON []byte) {
		lastEnvelope = envelope
	})

	err := errors.New("sample error for testing client")
	client.LogError(err, "ERROR", nil, map[string]any{"key": "value"})
	client.Flush()

	assert.NotNil(t, lastEnvelope)
	assert.NotEmpty(t, lastEnvelope.Entries)
	entry := lastEnvelope.Entries[0]
	assert.Equal(t, "sample error for testing client", entry.Message)
	assert.Equal(t, "ERROR", entry.Severity)
	assert.NotNil(t, entry.Error)
	assert.Equal(t, "*errors.errorString", entry.Error.Type)
}
