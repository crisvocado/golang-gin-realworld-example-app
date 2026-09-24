package logcore

import (
	"github.com/gin-gonic/gin"
)

// Middleware returns a Gin middleware using the default Logcore client.
func Middleware() gin.HandlerFunc {
	return MiddlewareWithClient(GetDefaultClient())
}

// MiddlewareWithClient returns a Gin middleware using the specified Logcore client.
// It extracts tracing headers, catches panics to forward them to Logcore,
// and reports errors for requests resulting in HTTP 5xx or containing errors.
func MiddlewareWithClient(client *Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		trace := ExtractTrace(c.Request)
		if trace != nil {
			c.Set("logcore_trace", trace)
		}

		defer func() {
			if rec := recover(); rec != nil {
				client.LogPanic(c, rec, trace)
				// Re-panic so gin.Recovery() or upstream error handlers can complete response
				panic(rec)
			}
		}()

		c.Next()

		if len(c.Errors) > 0 || c.Writer.Status() >= 500 {
			client.LogGinError(c, trace)
		}
	}
}
