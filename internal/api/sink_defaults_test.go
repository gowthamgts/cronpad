package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestWithSinkDefaultsFillsOnlyWhatIsMissing(t *testing.T) {
	c, err := config.Load(context.Background(), "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := func(kind, raw string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(withSinkDefaults(c, kind, json.RawMessage(raw)), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := got("webhook", `{"url":"https://example.com"}`)
	if b, _ := json.Marshal(m["success_codes"]); string(b) != `["2xx"]` {
		t.Errorf("success_codes %s", b)
	}
	if b, _ := json.Marshal(m["retry_on"]); string(b) != `["5xx",408,429,"network","timeout"]` {
		t.Errorf("retry_on %s", b)
	}
	// An explicit empty list is the job's own choice.
	m = got("webhook", `{"url":"https://example.com","retry_on":[],"success_codes":[200,"3xx"]}`)
	if b, _ := json.Marshal(m["retry_on"]); string(b) != `[]` {
		t.Errorf("retry_on %s", b)
	}
	if b, _ := json.Marshal(m["success_codes"]); string(b) != `[200,"3xx"]` {
		t.Errorf("success_codes %s", b)
	}
	m = got("command", `{"command":"true"}`)
	if b, _ := json.Marshal([]any{m["success_exit_codes"], m["retry_exit_codes"]}); string(b) != `[[0],[]]` {
		t.Errorf("exit codes %s", b)
	}
}
