package logcore

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	defaultTransport *Transport
	transportOnce    sync.Once
)

// GetDefaultTransport returns the singleton Transport instance.
func GetDefaultTransport() *Transport {
	transportOnce.Do(func() {
		cfg := LoadConfigFromEnv()
		defaultTransport = NewTransport(cfg)
	})
	return defaultTransport
}

// Init initializes the default transport with custom configuration.
func Init(cfg Config) {
	if defaultTransport != nil {
		defaultTransport.Close()
	}
	defaultTransport = NewTransport(cfg)
}

// SetCaptureHook registers a capture callback on the default transport.
func SetCaptureHook(hook func(payload []byte)) {
	GetDefaultTransport().SetCaptureHook(hook)
}

// SetSender registers a custom HTTP sender on the default transport.
func SetSender(sender func(req *http.Request) (*http.Response, error)) {
	GetDefaultTransport().SetSender(sender)
}

// Flush flushes all buffered entries on the default transport.
func Flush() {
	GetDefaultTransport().Flush()
}

// Close gracefully shuts down the default transport.
func Close() {
	if defaultTransport != nil {
		defaultTransport.Close()
	}
}

// Shutdown flushes and terminates the default transport respecting context timeout.
func Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		Close()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

// Error logs an error event to Logcore.
func Error(err error, message string, ctx map[string]interface{}) {
	Log("ERROR", message, err, ctx)
}

// Log logs a structured event with the specified severity to Logcore.
func Log(severity, message string, err error, ctx map[string]interface{}) {
	var errInfo *ErrorInfo
	if err != nil {
		errType := strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
		stack := captureStack(3, maxStackFrames)
		errInfo = &ErrorInfo{
			Type:    errType,
			Message: err.Error(),
			Stack:   stack,
		}
	}

	entry := &LogEntry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  severity,
		Message:   message,
		Error:     errInfo,
		Context:   ctx,
	}

	GetDefaultTransport().Enqueue(entry)
}

// GetTraceContext extracts the TraceContext stored in a Gin context, if available.
func GetTraceContext(c *gin.Context) *TraceContext {
	val, exists := c.Get(ContextKeyTrace)
	if !exists {
		return nil
	}
	tc, ok := val.(*TraceContext)
	if !ok {
		return nil
	}
	return tc
}

// GetTraceparent returns the W3C traceparent header string from context if available.
func GetTraceparent(c *gin.Context) string {
	tc := GetTraceContext(c)
	if tc == nil {
		return ""
	}
	span := "0000000000000000"
	if tc.SpanID != nil {
		span = *tc.SpanID
	}
	flag := "00"
	if tc.Sampled {
		flag = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", tc.TraceID, span, flag)
}
