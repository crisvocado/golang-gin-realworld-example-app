package logcore

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

// ExtractTrace extracts W3C traceparent or alternative trace headers from incoming request.
func ExtractTrace(req *http.Request) *TraceContext {
	if req == nil {
		return nil
	}

	// 1. W3C traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
	tp := req.Header.Get("traceparent")
	if tp != "" {
		parts := strings.Split(tp, "-")
		if len(parts) >= 4 && len(parts[1]) == 32 && len(parts[2]) == 16 {
			sampled := parts[3] == "01" || parts[3] == "1"
			return &TraceContext{
				TraceID: parts[1],
				SpanID:  parts[2],
				Sampled: sampled,
			}
		}
	}

	// 2. X-Cloud-Trace-Context: TRACE_ID/SPAN_ID;o=TRACE_TRUE
	xct := req.Header.Get("X-Cloud-Trace-Context")
	if xct != "" {
		parts := strings.Split(xct, "/")
		if len(parts) >= 1 && len(parts[0]) > 0 {
			traceID := parts[0]
			var spanID string
			sampled := false
			if len(parts) > 1 {
				spanParts := strings.Split(parts[1], ";")
				spanID = spanParts[0]
				if len(spanParts) > 1 && strings.Contains(spanParts[1], "o=1") {
					sampled = true
				}
			}
			return &TraceContext{
				TraceID: traceID,
				SpanID:  spanID,
				Sampled: sampled,
			}
		}
	}

	// 3. X-Request-ID or X-Trace-ID
	xReqID := req.Header.Get("X-Request-ID")
	if xReqID == "" {
		xReqID = req.Header.Get("X-Trace-ID")
	}
	if xReqID != "" {
		clean := strings.ToLower(strings.ReplaceAll(xReqID, "-", ""))
		if len(clean) >= 16 {
			if len(clean) > 32 {
				clean = clean[:32]
			}
			return &TraceContext{
				TraceID: clean,
				Sampled: true,
			}
		}
	}

	return nil
}

// GenerateTrace creates a new TraceContext if none is provided.
func GenerateTrace() *TraceContext {
	var traceBytes [16]byte
	var spanBytes [8]byte
	_, _ = rand.Read(traceBytes[:])
	_, _ = rand.Read(spanBytes[:])

	return &TraceContext{
		TraceID: hex.EncodeToString(traceBytes[:]),
		SpanID:  hex.EncodeToString(spanBytes[:]),
		Sampled: true,
	}
}
