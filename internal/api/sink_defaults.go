package api

import (
	"encoding/json"
	"strconv"

	"github.com/gowthamgts/cronpad/internal/config"
)

// withSinkDefaults writes the configured starting codes into a job's sink config wherever the job
// doesn't set them, so each job keeps its own copy and a later config change never alters it.
// An explicit empty list is the job's choice and stays as is.
func withSinkDefaults(c config.Config, kind string, raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	missing := func(key string) bool {
		v, ok := fields[key]
		return !ok || string(v) == "null"
	}
	set := func(key string, value any) {
		b, _ := json.Marshal(value)
		fields[key] = b
	}
	switch kind {
	case "webhook":
		if missing("success_codes") {
			set("success_codes", codeTokens(c.Sinks.Webhook.SuccessCodes))
		}
		if missing("retry_on") {
			set("retry_on", codeTokens(c.Sinks.Webhook.RetryOn))
		}
	case "command":
		if missing("success_exit_codes") {
			set("success_exit_codes", nonNil(c.Sinks.Command.SuccessExitCodes))
		}
		if missing("retry_exit_codes") {
			set("retry_exit_codes", nonNil(c.Sinks.Command.RetryExitCodes))
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

// codeTokens turns "503" into 503 and leaves classes and keywords ("5xx", "network") as strings.
func codeTokens(tokens []string) []any {
	out := make([]any, 0, len(tokens))
	for _, t := range tokens {
		if n, err := strconv.Atoi(t); err == nil {
			out = append(out, n)
		} else {
			out = append(out, t)
		}
	}
	return out
}

func nonNil(v []int) []int {
	if v == nil {
		return []int{}
	}
	return v
}
