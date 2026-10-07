package logcore

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const TraceContextKey = "logcore_trace"

// Middleware creates a Gin middleware that extracts W3C traceparent headers,
// catches panics and request errors, and forwards them to Logcore.
func Middleware() gin.HandlerFunc {
	client := GetClient()

	return func(c *gin.Context) {
		trace := ParseTraceparent(c.GetHeader("traceparent"))
		if trace != nil {
			c.Set(TraceContextKey, trace)
		}

		defer func() {
			if r := recover(); r != nil {
				var errObj *ErrorDetails
				if err, ok := r.(error); ok {
					errObj = &ErrorDetails{
						Type:    fmt.Sprintf("%T", err),
						Message: err.Error(),
						Stack:   captureStack(3),
					}
				} else {
					errObj = &ErrorDetails{
						Type:    "panic",
						Message: fmt.Sprintf("%v", r),
						Stack:   captureStack(3),
					}
				}

				ctxMap := map[string]any{
					"http": map[string]any{
						"method": c.Request.Method,
						"path":   c.Request.URL.Path,
						"ip":     c.ClientIP(),
					},
				}

				entry := LogEntry{
					Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					Severity:  "CRITICAL",
					Message:   fmt.Sprintf("Panic recovered: %v", r),
					Service:   client.config.Service,
					Env:       client.config.Env,
					Trace:     trace,
					Error:     errObj,
					Context:   ctxMap,
				}
				entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)

				client.Log(entry)

				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()

		c.Next()

		if len(c.Errors) > 0 {
			for _, ginErr := range c.Errors {
				errObj := &ErrorDetails{
					Type:    fmt.Sprintf("%T", ginErr.Err),
					Message: ginErr.Err.Error(),
					Stack:   captureStack(3),
				}
				ctxMap := map[string]any{
					"http": map[string]any{
						"method":      c.Request.Method,
						"path":        c.Request.URL.Path,
						"status_code": c.Writer.Status(),
						"ip":          c.ClientIP(),
					},
				}
				entry := LogEntry{
					Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					Severity:  "ERROR",
					Message:   ginErr.Error(),
					Service:   client.config.Service,
					Env:       client.config.Env,
					Trace:     trace,
					Error:     errObj,
					Context:   ctxMap,
				}
				entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
				client.Log(entry)
			}
		} else if c.Writer.Status() >= 500 {
			ctxMap := map[string]any{
				"http": map[string]any{
					"method":      c.Request.Method,
					"path":        c.Request.URL.Path,
					"status_code": c.Writer.Status(),
					"ip":          c.ClientIP(),
				},
			}
			entry := LogEntry{
				Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Severity:  "ERROR",
				Message:   fmt.Sprintf("HTTP %d error on %s %s", c.Writer.Status(), c.Request.Method, c.Request.URL.Path),
				Service:   client.config.Service,
				Env:       client.config.Env,
				Trace:     trace,
				Context:   ctxMap,
			}
			entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
			client.Log(entry)
		}
	}
}
