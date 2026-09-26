package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gothinkster/golang-gin-realworld-example-app/common/logcore"
	"github.com/stretchr/testify/assert"
)

func TestProbeEmission(t *testing.T) {
	var receivedBody []byte
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	os.Setenv("LOGCORE_URL", server.URL)
	os.Setenv("LOGCORE_KEY", "probe-key")
	os.Setenv("LOGCORE_ENABLED", "true")
	defer func() {
		os.Unsetenv("LOGCORE_URL")
		os.Unsetenv("LOGCORE_KEY")
		os.Unsetenv("LOGCORE_ENABLED")
	}()

	client := logcore.NewClientFromEnv()
	assert.True(t, client.IsEnabled())

	err := generateProbeError()
	client.Emit(logcore.LogEntry{
		Severity: "ERROR",
		Message:  err.Error(),
		Error: &logcore.ErrorEntry{
			Type:    "ProbeError",
			Message: err.Error(),
			Stack: []logcore.StackFrame{
				{
					Function: "TestProbeEmission",
					File:     "main_test.go",
					Line:     40,
					Column:   0,
					InApp:    true,
				},
			},
		},
		Context: map[string]interface{}{
			"probe": true,
		},
	})
	client.Flush()

	assert.Equal(t, "probe-key", receivedHeaders.Get("x-api-key"))
	assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))

	var envelope logcore.Envelope
	err = json.Unmarshal(receivedBody, &envelope)
	assert.NoError(t, err)
	assert.Equal(t, 1, envelope.SchemaVersion)
	assert.Len(t, envelope.Entries, 1)
	assert.Equal(t, "ERROR", envelope.Entries[0].Severity)
	assert.Equal(t, "golang-gin-realworld-example-app", envelope.Entries[0].Service)
}
