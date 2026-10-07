package logcore

import (
	"fmt"
	"sync"
	"time"
)

type Client struct {
	config    Config
	transport *HTTPTransport
	mu        sync.RWMutex
}

var (
	defaultClient *Client
	once          sync.Once
)

func GetClient() *Client {
	once.Do(func() {
		cfg := LoadConfig()
		defaultClient = &Client{
			config:    cfg,
			transport: NewHTTPTransport(cfg),
		}
	})
	return defaultClient
}

func (c *Client) Config() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}

func (c *Client) SetConfig(cfg Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config = cfg
}

func (c *Client) Transport() *HTTPTransport {
	return c.transport
}

func (c *Client) Log(entry LogEntry) {
	if !c.config.Enabled {
		return
	}
	if !shouldLog(entry.Severity, c.config.MinSeverity) {
		return
	}

	if entry.Timestamp == "" {
		entry.Timestamp = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if entry.Service == "" {
		entry.Service = c.config.Service
	}
	if entry.Env == "" {
		entry.Env = c.config.Env
	}
	if entry.InsertID == "" {
		entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	}

	clampEntry(&entry)
	c.transport.Enqueue(entry)
}

func (c *Client) LogError(err error, severity string, trace *TraceContext, ctxMap map[string]any) {
	if err == nil {
		return
	}
	if severity == "" {
		severity = "ERROR"
	}
	entry := LogEntry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  severity,
		Message:   err.Error(),
		Service:   c.config.Service,
		Env:       c.config.Env,
		Trace:     trace,
		Error: &ErrorDetails{
			Type:    fmt.Sprintf("%T", err),
			Message: err.Error(),
			Stack:   captureStack(3),
		},
		Context: ctxMap,
	}
	entry.InsertID = computeInsertID(entry.Timestamp, entry.Service, entry.Severity, entry.Message, entry.Context)
	c.Log(entry)
}

func (c *Client) Flush() {
	if c.transport != nil {
		c.transport.Flush()
	}
}

func (c *Client) Close() {
	if c.transport != nil {
		c.transport.Close()
	}
}

// Package-level convenience functions
func Log(entry LogEntry) {
	GetClient().Log(entry)
}

func LogError(err error, severity string, trace *TraceContext, ctxMap map[string]any) {
	GetClient().LogError(err, severity, trace, ctxMap)
}

func Flush() {
	GetClient().Flush()
}

func Close() {
	GetClient().Close()
}
