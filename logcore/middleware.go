package logcore

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Middleware returns a Gin middleware handler that manages distributed tracing,
// recovers from panics, and forwards HTTP 5xx / c.Errors to Logcore.
func Middleware() gin.HandlerFunc {
	return GinMiddleware()
}

// GinMiddleware returns the Gin middleware for Logcore integration.
func GinMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1. Extract or generate W3C trace context
		traceHeader := c.GetHeader("traceparent")
		trace := ParseTraceparent(traceHeader)
		if trace == nil {
			trace = GenerateTrace()
		}
		c.Set("logcore_trace", trace)
		c.Header("traceparent", trace.Traceparent())

		// 2. Panic recovery handler
		defer func() {
			if r := recover(); r != nil {
				stack := CaptureStack(3, maxFramesCount)

				var errType string
				var errMsg string
				if err, ok := r.(error); ok {
					errType = fmt.Sprintf("%T", err)
					errMsg = err.Error()
				} else {
					errType = fmt.Sprintf("%T", r)
					errMsg = fmt.Sprintf("%v", r)
				}

				entry := &Entry{
					Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					Severity:  SeverityCritical,
					Message:   fmt.Sprintf("Panic in %s %s: %s", c.Request.Method, c.Request.URL.Path, errMsg),
					Trace:     trace,
					Error: &ErrorInfo{
						Type:    errType,
						Message: errMsg,
						Stack:   stack,
					},
					Context: map[string]interface{}{
						"http": map[string]interface{}{
							"method":      c.Request.Method,
							"path":        c.Request.URL.Path,
							"query":       c.Request.URL.RawQuery,
							"status_code": http.StatusInternalServerError,
							"client_ip":   c.ClientIP(),
							"user_agent":  c.Request.UserAgent(),
						},
					},
				}

				GetDefaultClient().LogEntry(entry)

				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()

		// 3. Process the request
		c.Next()

		// 4. Capture server errors and explicit c.Errors
		status := c.Writer.Status()
		hasErrors := len(c.Errors) > 0

		if status >= 500 || (status >= 400 && hasErrors) {
			var message string
			var lastErr error
			if hasErrors {
				message = c.Errors.String()
				lastErr = c.Errors.Last().Err
			} else {
				message = fmt.Sprintf("HTTP %d error on %s %s", status, c.Request.Method, c.Request.URL.Path)
				lastErr = fmt.Errorf("HTTP status %d", status)
			}

			var errInfo *ErrorInfo
			if lastErr != nil {
				rootErr := UnwrapRootError(lastErr)
				errInfo = &ErrorInfo{
					Type:    fmt.Sprintf("%T", rootErr),
					Message: rootErr.Error(),
					Stack:   CaptureStack(2, maxFramesCount),
				}
			}

			entry := &Entry{
				Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Severity:  SeverityError,
				Message:   message,
				Trace:     trace,
				Error:     errInfo,
				Context: map[string]interface{}{
					"http": map[string]interface{}{
						"method":      c.Request.Method,
						"path":        c.Request.URL.Path,
						"query":       c.Request.URL.RawQuery,
						"status_code": status,
						"client_ip":   c.ClientIP(),
						"user_agent":  c.Request.UserAgent(),
					},
				},
			}

			GetDefaultClient().LogEntry(entry)
		}
	}
}
