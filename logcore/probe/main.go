package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/gothinkster/golang-gin-realworld-example-app/logcore"
)

func main() {
	client := logcore.DefaultClient()

	err := errors.New("simulated logcore probe error")
	stack := logcore.CaptureStack(0)

	entry := client.NewEntry(
		"ERROR",
		err.Error(),
		&logcore.ErrorInfo{
			Type:    fmt.Sprintf("%T", err),
			Message: err.Error(),
			Stack:   stack,
		},
		&logcore.TraceInfo{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
			SpanID:  "00f067aa0ba902b7",
			Sampled: true,
		},
		map[string]any{
			"component": "probe",
			"probe":     true,
		},
	)

	data, marshalErr := json.Marshal(entry)
	if marshalErr != nil {
		fmt.Fprintf(os.Stderr, "failed to marshal probe entry: %v\n", marshalErr)
		os.Exit(1)
	}

	fmt.Println(string(data))
}
