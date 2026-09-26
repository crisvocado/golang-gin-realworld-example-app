package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/gothinkster/golang-gin-realworld-example-app/common/logcore"
)

func generateProbeError() error {
	return errors.New("logcore probe error: verifying structured error reporting")
}

func main() {
	err := generateProbeError()
	entry := logcore.BuildErrorEntry(err, map[string]interface{}{
		"probe":     true,
		"component": "logcore_probe",
	})

	jsonBytes, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal log entry: %v\n", err)
		os.Exit(1)
	}

	// Output the single JSON log line to stdout for validation
	fmt.Println(string(jsonBytes))

	// Also emit through the configured client if enabled
	client := logcore.DefaultClient()
	if client != nil && client.IsEnabled() {
		client.Emit(entry)
		client.Flush()
	}
}
