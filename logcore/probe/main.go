package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/gothinkster/golang-gin-realworld-example-app/logcore"
)

func main() {
	gin.SetMode(gin.ReleaseMode)

	var captured []byte
	logcore.SetCaptureHook(func(payload []byte) {
		captured = payload
	})

	r := gin.New()
	r.Use(logcore.Middleware())

	r.GET("/api/probe-error", func(c *gin.Context) {
		_ = c.Error(errors.New("synthetic probe emission verification error"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "probe error"})
	})

	req, err := http.NewRequest("GET", "/api/probe-error", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create probe request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	logcore.Flush()

	if len(captured) == 0 {
		fmt.Fprintf(os.Stderr, "probe failed: no envelope captured\n")
		os.Exit(1)
	}

	_, _ = os.Stdout.Write(captured)
	fmt.Println()
}
