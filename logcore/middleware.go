package logcore

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	defaultClient *Client
	defaultOnce   sync.Once
	clientMu      sync.RWMutex
)

// LoadConfigFromEnv reads logcore configuration from environment variables.
func LoadConfigFromEnv() Config {
	url := os.Getenv("LOGCORE_URL")
	key := os.Getenv("LOGCORE_KEY")
	service := os.Getenv("LOGCORE_SERVICE")
	env := os.Getenv("LOGCORE_ENV")
	minSev := os.Getenv("LOGCORE_MIN_SEVERITY")
	enabledStr := os.Getenv("LOGCORE_ENABLED")

	enabled := true
	if strings.EqualFold(enabledStr, "false") || enabledStr == "0" {
		enabled = false
	}

	return Config{
		Enabled:       enabled,
		URL:           url,
		Key:           key,
		Service:       service,
		Env:           env,
		MinSeverity:   minSev,
		BatchSize:     20,
		FlushInterval: 5 * time.Second,
		MaxBuffer:     1000,
		MaxRetries:    3,
		HTTPTimeout:   10 * time.Second,
	}
}

// DefaultClient returns the shared singleton Client instance.
func DefaultClient() *Client {
	clientMu.RLock()
	if defaultClient != nil {
		clientMu.RUnlock()
		return defaultClient
	}
	clientMu.RUnlock()

	defaultOnce.Do(func() {
		clientMu.Lock()
		defaultClient = NewClient(LoadConfigFromEnv())
		clientMu.Unlock()
	})
	return defaultClient
}

// SetDefaultClient sets or overrides the default client (useful in tests).
func SetDefaultClient(c *Client) {
	clientMu.Lock()
	defaultClient = c
	clientMu.Unlock()
}

// Middleware returns a Gin middleware attached to the default logcore client.
func Middleware() gin.HandlerFunc {
	return MiddlewareWithClient(DefaultClient())
}

// MiddlewareWithClient returns a Gin middleware using the specified client.
func MiddlewareWithClient(client *Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client != nil && client.IsIngestURL(c.Request.URL.String()) {
			c.Next()
			return
		}

		start := time.Now()

		trace := parseTraceparent(c.GetHeader("traceparent"))
		if trace == nil {
			trace = parseTraceparent(c.GetHeader("Traceparent"))
		}

		if trace != nil {
			c.Set("logcore_trace", trace)
		}

		defer func() {
			if r := recover(); r != nil {
				stack := CaptureStack(3)

				var errMsg string
				var errType string
				switch v := r.(type) {
				case error:
					errMsg = v.Error()
					errType = fmt.Sprintf("%T", v)
				case string:
					errMsg = v
					errType = "panic"
				default:
					errMsg = fmt.Sprintf("%v", v)
					errType = "panic"
				}

				if client != nil {
					entry := client.NewEntry(
						"ERROR",
						fmt.Sprintf("panic recovered: %s", errMsg),
						&ErrorInfo{
							Type:    errType,
							Message: errMsg,
							Stack:   stack,
						},
						trace,
						map[string]any{
							"http": map[string]any{
								"method":      c.Request.Method,
								"path":        c.Request.URL.Path,
								"query":       c.Request.URL.RawQuery,
								"client_ip":   c.ClientIP(),
								"status_code": 500,
								"latency_ms":  time.Since(start).Milliseconds(),
								"user_agent":  c.Request.UserAgent(),
							},
						},
					)
					client.Enqueue(entry)
				}

				// Re-panic so Gin's recovery middleware / engine handles the HTTP response
				panic(r)
			}
		}()

		c.Next()

		status := c.Writer.Status()
		hasErrors := len(c.Errors) > 0

		if (status >= 500 || hasErrors) && client != nil {
			var errInfo *ErrorInfo
			var msg string

			if hasErrors {
				lastErr := c.Errors.Last()
				errType := "Error"
				if lastErr.Err != nil {
					errType = fmt.Sprintf("%T", lastErr.Err)
				}
				msg = lastErr.Error()
				errInfo = &ErrorInfo{
					Type:    errType,
					Message: msg,
					Stack:   CaptureStack(2),
				}
			} else {
				msg = fmt.Sprintf("HTTP %d error on %s %s", status, c.Request.Method, c.Request.URL.Path)
				errInfo = &ErrorInfo{
					Type:    "HTTPError",
					Message: msg,
					Stack:   CaptureStack(2),
				}
			}

			entry := client.NewEntry(
				"ERROR",
				msg,
				errInfo,
				trace,
				map[string]any{
					"http": map[string]any{
						"method":      c.Request.Method,
						"path":        c.Request.URL.Path,
						"query":       c.Request.URL.RawQuery,
						"client_ip":   c.ClientIP(),
						"status_code": status,
						"latency_ms":  time.Since(start).Milliseconds(),
						"user_agent":  c.Request.UserAgent(),
					},
				},
			)
			client.Enqueue(entry)
		}
	}
}

// parseTraceparent extracts W3C traceparent header components (00-traceId-spanId-flags).
func parseTraceparent(header string) *TraceInfo {
	if header == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(header), "-")
	if len(parts) < 4 {
		return nil
	}
	traceID := parts[1]
	spanID := parts[2]
	flags := parts[3]

	if len(traceID) != 32 || len(spanID) != 16 {
		return nil
	}

	return &TraceInfo{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: flags == "01" || flags == "1",
	}
}

// Flush flushes any buffered entries in the default client.
func Flush() {
	if defaultClient != nil {
		defaultClient.Flush()
	}
}

// Close gracefully shuts down the default client.
func Close() {
	if defaultClient != nil {
		defaultClient.Close()
	}
}
