package logcore

import (
	"os"
	"regexp"
	"strings"
)

var (
	validEnvMap = map[string]string{
		"prod":        "prod",
		"production":  "prod",
		"staging":     "staging",
		"dev":         "dev",
		"development": "dev",
		"test":        "test",
		"testing":     "test",
		"local":       "local",
	}

	validServiceRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

	severityLevels = map[string]int{
		"DEBUG":     100,
		"INFO":      200,
		"NOTICE":    300,
		"WARNING":   400,
		"ERROR":     500,
		"CRITICAL":  600,
		"ALERT":     700,
		"EMERGENCY": 800,
	}
)

type Config struct {
	URL         string
	Key         string
	Enabled     bool
	Service     string
	Env         string
	MinSeverity string
}

func LoadConfig() Config {
	url := os.Getenv("LOGCORE_URL")
	if url == "" {
		url = "https://logcore-dev-352942961463.us-east4.run.app"
	}
	url = strings.TrimRight(url, "/")

	key := os.Getenv("LOGCORE_KEY")

	enabledStr := strings.ToLower(os.Getenv("LOGCORE_ENABLED"))
	enabled := true
	if enabledStr == "false" || enabledStr == "0" || enabledStr == "no" {
		enabled = false
	}

	service := os.Getenv("LOGCORE_SERVICE")
	if service == "" {
		service = "golang-gin-realworld-example-app"
	}
	service = strings.ToLower(service)
	if !validServiceRegex.MatchString(service) {
		service = "golang-gin-realworld-example-app"
	}

	rawEnv := strings.ToLower(os.Getenv("LOGCORE_ENV"))
	env, ok := validEnvMap[rawEnv]
	if !ok {
		env = "dev"
	}

	minSeverity := strings.ToUpper(os.Getenv("LOGCORE_MIN_SEVERITY"))
	if minSeverity == "" {
		minSeverity = "ERROR"
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

func shouldLog(severity, minSeverity string) bool {
	sevVal, ok := severityLevels[strings.ToUpper(severity)]
	if !ok {
		sevVal = 500
	}
	minVal, ok := severityLevels[strings.ToUpper(minSeverity)]
	if !ok {
		minVal = 500
	}
	return sevVal >= minVal
}
