package logcore

import (
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	ContextKeyTrace = "logcore_trace_context"
)

// Middleware returns a Gin middleware that extracts trace headers and logs unhandled errors to Logcore.
func Middleware() gin.HandlerFunc {
	return MiddlewareWithTransport(GetDefaultTransport())
}

// MiddlewareWithTransport returns a Gin middleware attached to a specific Logcore transport.
func MiddlewareWithTransport(transport *Transport) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract incoming W3C traceparent header
		traceparent := c.GetHeader("traceparent")
		traceCtx := parseTraceparent(traceparent)
		if traceCtx != nil {
			c.Set(ContextKeyTrace, traceCtx)
		}

		// Ensure recovery from panics within route handlers
		defer func() {
			if r := recover(); r != nil {
				err, ok := r.(error)
				var errMsg, errType string
				if ok {
					errMsg = err.Error()
					errType = strings.TrimPrefix(fmt.Sprintf("%T", err), "*")
				} else {
					errMsg = fmt.Sprintf("%v", r)
					errType = "panic"
				}

				stack := captureStack(3, maxStackFrames)
				entry := &LogEntry{
					Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
					Severity:  "CRITICAL",
					Message:   fmt.Sprintf("Panic recovered: %s", errMsg),
					Trace:     traceCtx,
					Error: &ErrorInfo{
						Type:    errType,
						Message: errMsg,
						Stack:   stack,
					},
					Context: map[string]interface{}{
						"http_method": c.Request.Method,
						"http_path":   c.Request.URL.Path,
						"status_code": http.StatusInternalServerError,
						"client_ip":   c.ClientIP(),
					},
				}

				transport.Enqueue(entry)

				// Abort request with 500
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()

		c.Next()

		// Check for Gin errors or HTTP >= 500 status
		status := c.Writer.Status()
		hasErrors := len(c.Errors) > 0

		if hasErrors || status >= 500 {
			severity := "ERROR"
			if status >= 500 {
				severity = "CRITICAL"
			}

			var errInfo *ErrorInfo
			var logMsg string

			if hasErrors {
				lastErr := c.Errors.Last().Err
				errType := strings.TrimPrefix(fmt.Sprintf("%T", lastErr), "*")
				errMsg := lastErr.Error()
				stack := captureStack(2, maxStackFrames)

				errInfo = &ErrorInfo{
					Type:    errType,
					Message: errMsg,
					Stack:   stack,
				}
				logMsg = c.Errors.String()
			} else {
				logMsg = fmt.Sprintf("HTTP %d error handling %s %s", status, c.Request.Method, c.Request.URL.Path)
			}

			reqContext := map[string]interface{}{
				"http_method": c.Request.Method,
				"http_path":   c.Request.URL.Path,
				"status_code": status,
				"client_ip":   c.ClientIP(),
			}

			entry := &LogEntry{
				Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
				Severity:  severity,
				Message:   logMsg,
				Trace:     traceCtx,
				Error:     errInfo,
				Context:   reqContext,
			}

			transport.Enqueue(entry)
		}
	}
}

// parseTraceparent parses a W3C traceparent header into a TraceContext object.
func parseTraceparent(tp string) *TraceContext {
	tp = strings.TrimSpace(tp)
	if tp == "" {
		return nil
	}

	parts := strings.Split(tp, "-")
	if len(parts) != 4 {
		return nil
	}

	if len(parts[0]) != 2 {
		return nil
	}

	traceID := strings.ToLower(parts[1])
	if len(traceID) != 32 || traceID == "00000000000000000000000000000000" {
		return nil
	}
	for _, c := range traceID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return nil
		}
	}

	spanID := strings.ToLower(parts[2])
	var spanIDPtr *string
	if len(spanID) == 16 && spanID != "0000000000000000" {
		valid := true
		for _, c := range spanID {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				valid = false
				break
			}
		}
		if valid {
			spanIDPtr = &spanID
		}
	}

	flags := parts[3]
	sampled := false
	if len(flags) == 2 {
		if b, err := strconv.ParseUint(flags, 16, 8); err == nil {
			sampled = (b & 0x01) == 1
		}
	}

	return &TraceContext{
		TraceID: traceID,
		SpanID:  spanIDPtr,
		Sampled: sampled,
	}
}

// captureStack captures caller stack frames innermost-first.
func captureStack(skip int, maxFrames int) []*StackFrame {
	if maxFrames <= 0 || maxFrames > maxStackFrames {
		maxFrames = maxStackFrames
	}

	rpc := make([]uintptr, maxFrames+skip)
	n := runtime.Callers(skip, rpc)
	if n == 0 {
		return nil
	}

	frames := runtime.CallersFrames(rpc[:n])
	var result []*StackFrame

	for {
		frame, more := frames.Next()
		funcName := frame.Function
		file := frame.File
		line := frame.Line

		// Skip runtime/logcore internal callers
		if strings.Contains(funcName, "logcore.captureStack") || strings.Contains(funcName, "runtime.Callers") {
			if !more {
				break
			}
			continue
		}

		inApp := true
		if strings.Contains(file, "/go/pkg/mod/") ||
			strings.Contains(file, "/usr/local/go/") ||
			strings.Contains(file, "/pkg/mod/") ||
			strings.Contains(file, "runtime/") ||
			strings.HasPrefix(funcName, "runtime.") ||
			strings.Contains(funcName, "gin-gonic/gin") ||
			strings.Contains(funcName, "stretchr/testify") {
			inApp = false
		}

		sf := &StackFrame{
			Function: funcName,
			File:     &file,
			Line:     &line,
			InApp:    inApp,
		}
		result = append(result, sf)

		if len(result) >= maxFrames || !more {
			break
		}
	}

	return result
}
