package logcore

import (
	"path/filepath"
	"runtime"
	"strings"
)

// CaptureStack extracts parsed stack frames from the runtime caller chain.
// Frames are ordered innermost first (index 0 is the immediate caller).
func CaptureStack(skip int) []StackFrame {
	const maxFrames = 50
	pc := make([]uintptr, maxFrames+skip+2)
	n := runtime.Callers(skip+1, pc)
	if n == 0 {
		return nil
	}

	frames := runtime.CallersFrames(pc[:n])
	var stack []StackFrame

	for {
		frame, more := frames.Next()

		// Skip internal logcore frames
		if strings.Contains(frame.Function, "golang-gin-realworld-example-app/logcore") &&
			!strings.Contains(frame.Function, "Test") {
			if !more {
				break
			}
			continue
		}

		inApp := isInApp(frame.File, frame.Function)

		stack = append(stack, StackFrame{
			Function: frame.Function,
			File:     frame.File,
			Line:     frame.Line,
			Column:   0,
			InApp:    inApp,
		})

		if !more || len(stack) >= maxFrames {
			break
		}
	}

	return stack
}

func isInApp(file, function string) bool {
	if file == "" {
		return false
	}
	// Check standard library / runtime
	if strings.HasPrefix(file, runtime.GOROOT()) {
		return false
	}
	// Check module / vendor cache
	if strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/vendor/") {
		return false
	}
	if strings.Contains(function, "github.com/gin-gonic/gin") ||
		strings.Contains(function, "gorm.io/") ||
		strings.Contains(function, "net/http") ||
		strings.Contains(function, "runtime.") {
		return false
	}
	// Application module frames
	if strings.Contains(function, "github.com/gothinkster/golang-gin-realworld-example-app") {
		return true
	}
	// Workspace repository files
	clean := filepath.Clean(file)
	if !strings.HasPrefix(clean, "/usr/") && !strings.HasPrefix(clean, "/opt/") {
		return true
	}
	return false
}
