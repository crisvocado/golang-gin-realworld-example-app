package logcore

import (
	"fmt"
	"sync"
	"time"
)

// Client is the primary interface for logging errors to Logcore.
type Client struct {
	config    Config
	transport *HTTPTransport
}

var (
	defaultClient *Client
	defaultOnce   sync.Once
	mu            sync.RWMutex
)

// Option allows customizing the Client configuration.
type Option func(*Config)

// WithURL sets a custom endpoint URL.
func WithURL(url string) Option {
	return func(c *Config) {
		c.URL = url
	}
}

// WithKey sets the API key.
func WithKey(key string) Option {
	return func(c *Config) {
		c.Key = key
	}
}

// WithService sets the service name.
func WithService(service string) Option {
	return func(c *Config) {
		c.Service = sanitizeServiceName(service)
	}
}

// WithEnv sets the environment name.
func WithEnv(env string) Option {
	return func(c *Config) {
		c.Env = env
	}
}

// WithMinSeverity sets the minimum severity floor.
func WithMinSeverity(minSeverity string) Option {
	return func(c *Config) {
		c.MinSeverity = minSeverity
	}
}

// NewClient creates a new configured Logcore client.
func NewClient(cfg Config) *Client {
	transport := NewHTTPTransport(cfg)
	return &Client{
		config:    cfg,
		transport: transport,
	}
}

// Init initializes the global default Logcore client.
func Init(opts ...Option) *Client {
	mu.Lock()
	defer mu.Unlock()

	cfg := LoadConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if defaultClient != nil {
		_ = defaultClient.Close()
	}

	defaultClient = NewClient(cfg)
	return defaultClient
}

// GetDefaultClient returns the singleton Logcore client, initializing it with defaults if needed.
func GetDefaultClient() *Client {
	mu.RLock()
	client := defaultClient
	mu.RUnlock()

	if client != nil {
		return client
	}

	defaultOnce.Do(func() {
		cfg := LoadConfig()
		defaultClient = NewClient(cfg)
	})

	return defaultClient
}

// EntryOption allows customizing a single Logcore entry.
type EntryOption func(*Entry)

// WithTrace attaches trace context to an entry.
func WithTrace(trace *Trace) EntryOption {
	return func(e *Entry) {
		e.Trace = trace
	}
}

// WithContext adds key-value context to an entry.
func WithContext(ctx map[string]interface{}) EntryOption {
	return func(e *Entry) {
		if e.Context == nil {
			e.Context = make(map[string]interface{})
		}
		for k, v := range ctx {
			e.Context[k] = v
		}
	}
}

// WithLabels adds labels to an entry.
func WithLabels(labels map[string]string) EntryOption {
	return func(e *Entry) {
		if e.Labels == nil {
			e.Labels = make(map[string]string)
		}
		for k, v := range labels {
			e.Labels[k] = v
		}
	}
}

// WithSeverity sets entry severity.
func WithSeverity(severity string) EntryOption {
	return func(e *Entry) {
		e.Severity = severity
	}
}

// LogEntry sends an entry through the client transport.
func (c *Client) LogEntry(entry *Entry) {
	if c == nil || c.transport == nil || entry == nil {
		return
	}
	c.transport.Send(entry)
}

// BuildEntry constructs and prepares a complete Entry struct ready for emission or serialization.
func (c *Client) BuildEntry(msg string, err error, opts ...EntryOption) *Entry {
	if c == nil {
		return nil
	}

	var errInfo *ErrorInfo
	if err != nil {
		rootErr := UnwrapRootError(err)
		errInfo = &ErrorInfo{
			Type:    fmt.Sprintf("%T", rootErr),
			Message: rootErr.Error(),
			Stack:   CaptureStack(1, maxStackFrames),
		}
	}

	entry := &Entry{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Severity:  SeverityError,
		Message:   msg,
		Service:   c.config.Service,
		Env:       c.config.Env,
		Error:     errInfo,
	}

	for _, opt := range opts {
		opt(entry)
	}

	if c.transport != nil {
		c.transport.prepareEntry(entry)
	}

	return entry
}

// Error records an error message with parsed stack frames.
func (c *Client) Error(msg string, err error, opts ...EntryOption) {
	if c == nil || c.transport == nil {
		return
	}

	entry := c.BuildEntry(msg, err, opts...)
	c.transport.Send(entry)
}

// Flush synchronously flushes any buffered entries.
func (c *Client) Flush(timeout time.Duration) error {
	if c == nil || c.transport == nil {
		return nil
	}
	return c.transport.Flush(timeout)
}

// Close flushes buffered entries and closes the transport.
func (c *Client) Close() error {
	if c == nil || c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

// Global convenience helpers

// BuildEntry constructs a prepared Entry on the default client.
func BuildEntry(msg string, err error, opts ...EntryOption) *Entry {
	return GetDefaultClient().BuildEntry(msg, err, opts...)
}

// Error records an error on the default client.
func Error(msg string, err error, opts ...EntryOption) {
	GetDefaultClient().Error(msg, err, opts...)
}

// LogEntry records a raw entry on the default client.
func LogEntry(entry *Entry) {
	GetDefaultClient().LogEntry(entry)
}

// Flush flushes the default client.
func Flush(timeout time.Duration) error {
	return GetDefaultClient().Flush(timeout)
}

// Close closes the default client.
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if defaultClient != nil {
		err := defaultClient.Close()
		defaultClient = nil
		return err
	}
	return nil
}
