package logcore

import (
	"os"
	"regexp"
	"strings"
)

const (
	DefaultEndpoint    = "https://logcore-dev-352942961463.us-east4.run.app"
	DefaultServiceName = "golang-gin-realworld-example-app"
	DefaultEnv         = "dev"
	DefaultMinSeverity = "ERROR"
)

var (
	validEnvMap = map[string]bool{
		"prod":    true,
		"staging": true,
		"dev":     true,
		"test":    true,
		"local":   true,
	}

	serviceRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

	severityLevels = map[string]int{
		"DEBUG":     0,
		"INFO":      1,
		"NOTICE":    2,
		"WARNING":   3,
		"ERROR":     4,
		"CRITICAL":  5,
		"ALERT":     6,
		"EMERGENCY": 7,
	}
)

// Config holds runtime configuration for Logcore.
type Config struct {
	URL         string
	Key         string
	Enabled     bool
	Service     string
	Env         string
	MinSeverity string
}

// LoadConfigFromEnv reads configuration from environment variables.
func LoadConfigFromEnv() Config {
	url := strings.TrimSpace(os.Getenv("LOGCORE_URL"))
	if url == "" {
		url = DefaultEndpoint
	}

	key := strings.TrimSpace(os.Getenv("LOGCORE_KEY"))

	enabledStr := strings.ToLower(strings.TrimSpace(os.Getenv("LOGCORE_ENABLED")))
	enabled := true
	if enabledStr == "false" || enabledStr == "0" || enabledStr == "off" || enabledStr == "no" {
		enabled = false
	}

	service := strings.ToLower(strings.TrimSpace(os.Getenv("LOGCORE_SERVICE")))
	if service == "" || !serviceRegex.MatchString(service) {
		service = DefaultServiceName
	}

	env := strings.ToLower(strings.TrimSpace(os.Getenv("LOGCORE_ENV")))
	if !validEnvMap[env] {
		env = DefaultEnv
	}

	minSeverity := strings.ToUpper(strings.TrimSpace(os.Getenv("LOGCORE_MIN_SEVERITY")))
	if _, ok := severityLevels[minSeverity]; !ok {
		minSeverity = DefaultMinSeverity
	}

	return Config{
		URL:         url,
		Key:         key,
		Enabled:     enabled,
		Service:     service,
		Env:         env,
		MinSeverity: minSeverity,
	}
}

// IsSeverityAllowed returns true if the given severity meets or exceeds the minimum severity.
func (c *Config) IsSeverityAllowed(severity string) bool {
	entryLevel, ok := severityLevels[strings.ToUpper(severity)]
	if !ok {
		return false
	}
	minLevel := severityLevels[c.MinSeverity]
	return entryLevel >= minLevel
}
