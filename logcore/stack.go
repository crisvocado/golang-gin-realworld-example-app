package logcore

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
)

const maxStackFrames = 50

// CaptureStack captures up to maxFrames stack frames starting from the specified skip depth.
// Innermost caller appears first (index 0).
func CaptureStack(skip int, maxFrames int) []Frame {
	if maxFrames <= 0 || maxFrames > maxStackFrames {
		maxFrames = maxStackFrames
	}

	rpc := make([]uintptr, maxFrames+skip+20)
	n := runtime.Callers(skip, rpc)
	if n == 0 {
		return nil
	}

	frames := runtime.CallersFrames(rpc[:n])
	var result []Frame

	for {
		frame, more := frames.Next()
		// Filter out adapter internal helper frames
		if !strings.Contains(frame.Function, "logcore.") && !strings.Contains(frame.File, "/logcore/") {
			inApp := isInApp(frame.File, frame.Function)
			result = append(result, Frame{
				Function: frame.Function,
				File:     frame.File,
				Line:     frame.Line,
				Column:   0,
				InApp:    inApp,
			})
		}
		if len(result) >= maxFrames || !more {
			break
		}
	}

	return result
}

// isInApp determines whether a stack frame belongs to the application rather than standard library or dependencies.
func isInApp(file, function string) bool {
	// Standard library runtime & core
	if strings.Contains(file, "/src/runtime/") || strings.HasPrefix(file, "runtime/") {
		return false
	}
	if strings.Contains(file, "/go/src/") || strings.Contains(file, "/usr/local/go/") {
		return false
	}
	// External dependencies downloaded to pkg/mod or vendor
	if strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/vendor/") {
		return false
	}
	// Adapter internal frames
	if strings.Contains(function, "logcore.") || strings.Contains(file, "/logcore/") {
		return false
	}
	// Project application packages (check module path or local paths)
	if strings.Contains(function, "golang-gin-realworld-example-app") ||
		strings.Contains(file, "golang-gin-realworld-example-app") ||
		strings.HasPrefix(function, "main.") {
		return true
	}
	// If it's not stdlib, not mod/vendor, not logcore, consider it application code
	return true
}

// UnwrapRootError traverses the errors.Unwrap chain to find the root cause error.
func UnwrapRootError(err error) error {
	if err == nil {
		return nil
	}
	root := err
	for {
		unwrapped := errors.Unwrap(root)
		if unwrapped == nil {
			break
		}
		root = unwrapped
	}
	return root
}

// ErrorToErrorInfo converts a Go error into an ErrorInfo struct with stack trace.
func ErrorToErrorInfo(err error, skipStack int) *ErrorInfo {
	if err == nil {
		return nil
	}
	return &ErrorInfo{
		Type:    fmt.Sprintf("%T", err),
		Message: err.Error(),
		Stack:   CaptureStack(skipStack+1, maxStackFrames),
	}
}
