package logcore

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

var traceparentRegex = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)

// ParseTraceparent parses a W3C traceparent header string into a Trace struct.
func ParseTraceparent(header string) *Trace {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	matches := traceparentRegex.FindStringSubmatch(strings.ToLower(header))
	if len(matches) != 5 {
		return nil
	}
	// version := matches[1]
	traceID := matches[2]
	spanID := matches[3]
	flags := matches[4]

	// All zeros are invalid in W3C spec
	if traceID == "00000000000000000000000000000000" || spanID == "0000000000000000" {
		return nil
	}

	return &Trace{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: flags == "01",
	}
}

// GenerateTrace generates a new Trace with cryptographically random traceId and spanId.
func GenerateTrace() *Trace {
	var traceBytes [16]byte
	var spanBytes [8]byte

	_, _ = rand.Read(traceBytes[:])
	_, _ = rand.Read(spanBytes[:])

	return &Trace{
		TraceID: hex.EncodeToString(traceBytes[:]),
		SpanID:  hex.EncodeToString(spanBytes[:]),
		Sampled: true,
	}
}
