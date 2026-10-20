package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gowthamgts/cronpad/internal/config"
)

func TestAlertCRUDWorkspaceIsolationIdempotencyAndAudit(t *testing.T) {
	f := newFixture(t)
	body := `{"kind":"webhook","name":"ops","config":{"url":"https://example.org/alert","format":"generic"}}`
	headers := map[string]string{"Idempotency-Key": "channel-one"}
	created := object(t, f.request("POST", "/api/v1/alert-channels", body, headers, nil, 201))
	channel := created["id"].(string)
	secret := created["signing_secret"].(string)
	replay := object(t, f.request("POST", "/api/v1/alert-channels", body, headers, nil, 201))
	if replay["id"] != channel || replay["signing_secret"] != secret {
		t.Fatal("channel idempotency failed")
	}
	f.request("POST", "/api/v1/alert-channels", strings.Replace(body, "ops", "other", 1), headers, nil, 409)
	current := object(t, f.request("GET", "/api/v1/alert-channels/"+channel, "", nil, nil, 200))
	if current["signing_secret"] != nil {
		t.Fatal("signing secret revealed twice")
	}
	var workspace string
	var stored string
	if err := f.a.DBs.Control.Reader.QueryRow("SELECT workspace_id,signing_secret FROM alert_channels WHERE id=?", channel).Scan(&workspace, &stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, secret) || !strings.Contains(stored, "encrypted") {
		t.Fatal("signing secret not encrypted")
	}
	f.request("PATCH", "/api/v1/alert-channels/"+channel, `{"name":"updated"}`, nil, nil, 200)
	ruleBody := fmt.Sprintf(`{"name":"failures","job_selector":{"all":true},"events":["run.failed"],"conditions":{"min_consecutive":2},"channel_ids":[%q],"cooldown":"15m"}`, channel)
	rule := object(t, f.request("POST", "/api/v1/alert-rules", ruleBody, map[string]string{"Idempotency-Key": "rule-one"}, nil, 201))
	ruleID := rule["id"].(string)
	replay = object(t, f.request("POST", "/api/v1/alert-rules", ruleBody, map[string]string{"Idempotency-Key": "rule-one"}, nil, 201))
	if replay["id"] != ruleID {
		t.Fatal("rule idempotency failed")
	}
	f.request("GET", "/api/v1/alert-rules/"+ruleID, "", nil, nil, 200)
	patched := object(t, f.request("PATCH", "/api/v1/alert-rules/"+ruleID, `{"enabled":false}`, nil, nil, 200))
	if patched["enabled"] != false {
		t.Fatal(patched)
	}
	for _, resource := range []struct {
		path    string
		methods []string
	}{{"/api/v1/alert-channels/" + channel, []string{"GET", "PATCH", "DELETE"}}, {"/api/v1/alert-rules/" + ruleID, []string{"GET", "PATCH", "DELETE"}}, {"/api/v1/alert-channels/" + channel + "/test", []string{"POST"}}, {"/api/v1/alert-channels/" + channel + "/verify", []string{"POST"}}} {
		for _, method := range resource.methods {
			requestBody := ""
			if method == "POST" || method == "PATCH" {
				requestBody = "{}"
			}
			f.request(method, resource.path, requestBody, map[string]string{"X-Dev-User": "bob"}, nil, 404)
		}
	}
	f.request("POST", "/api/v1/alert-rules", ruleBody, map[string]string{"X-Dev-User": "bob"}, nil, 404)
	f.request("GET", "/api/v1/alert-channels", "", nil, nil, 200)
	f.request("GET", "/api/v1/alert-rules", "", nil, nil, 200)
	f.request("DELETE", "/api/v1/alert-rules/"+ruleID, "", nil, nil, 204)
	f.request("DELETE", "/api/v1/alert-channels/"+channel, "", nil, nil, 204)
	var count int
	if err := f.a.DBs.History.Reader.QueryRow("SELECT count(*) FROM audit_log WHERE workspace_id=? AND action LIKE 'alert_%'", workspace).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("want 6 mutation audit rows, got %d", count)
	}
}
func TestCommandHostedMode400AndNotRegistered(t *testing.T) {
	f := newFixtureWith(t, func(c *config.Config) { c.Mode = "hosted"; c.Sinks.Command.Enabled = true })
	if f.a.Dispatcher.Workers.Sinks["command"] != nil {
		t.Fatal("hosted command sink registered")
	}
	f.request("POST", "/api/v1/jobs", `{"name":"blocked","schedule":{"expr":"@daily"},"sink":{"kind":"command","config":{"command":"/bin/echo","args":["blocked"]}}}`, nil, nil, 400)
	created := object(t, f.request("POST", "/api/v1/jobs", m3Job, nil, nil, 201))
	f.request("PATCH", "/api/v1/jobs/"+created["id"].(string), `{"sink":{"kind":"command","config":{"command":"/bin/echo"}}}`, nil, nil, 400)
	features := object(t, f.request("GET", "/api/v1/features", "", nil, nil, 200))
	if features["sinks"].(map[string]any)["command"] != false {
		t.Fatal(features)
	}
}
func TestCommandDisabledNotRegisteredAndSelfHostedWorks(t *testing.T) {
	disabled := newFixtureWith(t, func(c *config.Config) { c.Sinks.Command.Enabled = false })
	if disabled.a.Dispatcher.Workers.Sinks["command"] != nil {
		t.Fatal("disabled command registered")
	}
	disabled.request("POST", "/api/v1/jobs", `{"name":"blocked","schedule":{"expr":"@daily"},"sink":{"kind":"command","config":{"command":"/bin/echo"}}}`, nil, nil, 400)
	f := newFixture(t)
	if f.a.Dispatcher.Workers.Sinks["command"] == nil {
		t.Fatal("self-hosted command missing")
	}
	body := `{"name":"command","schedule":{"expr":"@daily"},"sink":{"kind":"command","config":{"command":"/bin/echo","args":["hello"]}}}`
	created := object(t, f.request("POST", "/api/v1/jobs", body, nil, nil, 201))
	path := "/api/v1/jobs/" + created["id"].(string)
	result := object(t, f.request("POST", path+"/test", "", nil, nil, 200))
	if result["outcome"] != "success" || result["response_body"] != "hello\n" {
		t.Fatal(result)
	}
	features := object(t, f.request("GET", "/api/v1/features", "", nil, nil, 200))
	if features["sinks"].(map[string]any)["command"] != true {
		t.Fatal(features)
	}
}
