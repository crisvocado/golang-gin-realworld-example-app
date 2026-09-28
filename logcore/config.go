package logcore

import (
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Default service configuration values
const (
	DefaultEndpoint    = "https://logcore-dev-352942961463.us-east4.run.app"
	DefaultServiceName = "golang-gin-realworld-example-app"
	DefaultEnv         = "dev"
	DefaultMinSeverity = SeverityError
)

var (
	serviceNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	validEnvs        = map[string]bool{
		"prod":    true,
		"staging": true,
		"dev":     true,
		"test":    true,
		"local":   true,
	}
)

// Config carries configuration for the Logcore client and transport.
type Config struct {
	URL         string
	Key         string
	Enabled     bool
	Service     string
	Env         string
	MinSeverity string
	HTTPClient  *http.Client
}

// LoadConfig reads configuration from standard environment variables.
func LoadConfig() Config {
	url := strings.TrimSpace(os.Getenv("LOGCORE_URL"))
	if url == "" {
		url = DefaultEndpoint
	}
	url = strings.TrimRight(url, "/")

	key := strings.TrimSpace(os.Getenv("LOGCORE_KEY"))

	enabledStr := strings.TrimSpace(os.Getenv("LOGCORE_ENABLED"))
	enabled := true
	if enabledStr == "false" || enabledStr == "0" {
		enabled = false
	}

	service := strings.TrimSpace(os.Getenv("LOGCORE_SERVICE"))
	if service == "" {
		service = DefaultServiceName
	}
	service = sanitizeServiceName(service)

	env := strings.TrimSpace(strings.ToLower(os.Getenv("LOGCORE_ENV")))
	if env == "production" {
		env = "prod"
	} else if env == "development" {
		env = "dev"
	}
	if !validEnvs[env] {
		env = DefaultEnv
	}

	minSeverity := strings.ToUpper(strings.TrimSpace(os.Getenv("LOGCORE_MIN_SEVERITY")))
	if _, ok := severityRanks[minSeverity]; !ok {
		minSeverity = DefaultMinSeverity
	}

	return Config{
		URL:         url,
		Key:         key,
		Enabled:     enabled,
		Service:     service,
		Env:         env,
		MinSeverity: minSeverity,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// sanitizeServiceName ensures service name matches ^[a-z0-9][a-z0-9._-]{0,62}$
func sanitizeServiceName(name string) string {
	name = strings.ToLower(name)
	var sb strings.Builder
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else if i > 0 && (r == '.' || r == '_' || r == '-') {
			sb.WriteRune(r)
		} else if i == 0 {
			sb.WriteRune('a')
		}
		if sb.Len() >= 63 {
			break
		}
	}
	res := sb.String()
	if !serviceNameRegex.MatchString(res) {
		return DefaultServiceName
	}
	return res
}
