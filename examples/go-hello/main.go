package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	event, err := os.ReadFile(os.Getenv("WERKT_EVENT_PATH"))
	if err != nil {
		panic(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(event, &envelope); err != nil {
		panic(err)
	}
	fmt.Printf("{\"message\":\"Go received an event\",\"eventId\":%q}\n", envelope["id"])
	result := map[string]any{
		"language": "go",
		"trigger":  envelope["trigger"],
		"received": envelope["data"],
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("WERKT_RESULT_PATH"), encoded, 0o600); err != nil {
		panic(err)
	}
}
