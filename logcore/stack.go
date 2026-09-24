package logcore

import (
	"errors"
	"fmt"
	"go/build"
	"path/filepath"
	"runtime"
	"strings"
)

const maxStackFrames = 50

// ParseCallers captures the current goroutine call stack, skipping skip frames.
func ParseCallers(skip int) []StackFrame {
	pcs := make([]uintptr, maxStackFrames+skip+2)
	n := runtime.Callers(skip+1, pcs)
	if n == 0 {
		return nil
	}
	return framesFromPCs(pcs[:n])
}

func framesFromPCs(pcs []uintptr) []StackFrame {
	iter := runtime.CallersFrames(pcs)
	var frames []StackFrame

	for {
		frame, more := iter.Next()
		// Filter out internal logcore adapter frames
		if strings.Contains(frame.Function, "golang-gin-realworld-example-app/logcore") {
			if !more {
				break
			}
			continue
		}

		inApp := isInApp(frame.File, frame.Function)
		frames = append(frames, StackFrame{
			Function: frame.Function,
			File:     frame.File,
			Line:     frame.Line,
			Column:   0,
			InApp:    inApp,
		})

		if len(frames) >= maxStackFrames || !more {
			break
		}
	}
	return frames
}

func isInApp(file, function string) bool {
	if file == "" {
		return false
	}
	// Standard library check
	goroot := runtime.GOROOT()
	if goroot != "" && strings.HasPrefix(file, goroot) {
		return false
	}
	if strings.HasPrefix(file, filepath.Join(build.Default.GOROOT, "src")) {
		return false
	}
	// Dependency packages
	if strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/vendor/") {
		return false
	}
	if strings.Contains(function, "github.com/gin-gonic/") ||
		strings.Contains(function, "gorm.io/") ||
		strings.Contains(function, "github.com/golang-jwt/") ||
		strings.Contains(function, "golang.org/") {
		return false
	}
	// Module application code
	if strings.Contains(function, "github.com/gothinkster/golang-gin-realworld-example-app") {
		return true
	}
	return true
}

// BuildErrorInfo constructs ErrorInfo from an error or panic value.
func BuildErrorInfo(errVal interface{}, skipCallers int) *ErrorInfo {
	if errVal == nil {
		return nil
	}

	var errType string
	var errMsg string

	switch v := errVal.(type) {
	case error:
		root := unwrapRoot(v)
		errType = fmt.Sprintf("%T", root)
		errMsg = v.Error()
	case string:
		errType = "panic"
		errMsg = v
	default:
		errType = fmt.Sprintf("%T", v)
		errMsg = fmt.Sprintf("%v", v)
	}

	frames := ParseCallers(skipCallers + 1)

	return &ErrorInfo{
		Type:    errType,
		Message: errMsg,
		Stack:   frames,
	}
}

func unwrapRoot(err error) error {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err
		}
		err = unwrapped
	}
}
