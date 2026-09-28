package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gothinkster/golang-gin-realworld-example-app/logcore"
)

func main() {
	client := logcore.Init()
	defer client.Close()

	probeErr := errors.New("intentional validation error for logcore integration probe")
	trace := logcore.GenerateTrace()

	entry := client.BuildEntry("logcore integration probe: verified error emission", probeErr,
		logcore.WithTrace(trace),
		logcore.WithContext(map[string]interface{}{
			"probe": map[string]interface{}{
				"source": "cmd/logcore_probe",
				"status": "active",
			},
		}),
		logcore.WithLabels(map[string]string{
			"component": "probe",
		}),
	)

	// Emit one JSON object per log line to stdout for validation
	if data, err := json.Marshal(entry); err == nil {
		fmt.Println(string(data))
	}

	// Dispatch through transport and flush
	client.LogEntry(entry)
	_ = client.Flush(2 * time.Second)
}
