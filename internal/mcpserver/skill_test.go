package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/schedule"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/sink/webhook"
)

func TestSkillWebhookSnippets(t *testing.T) {
	skill, e := os.ReadFile("../../skills/cronpad/SKILL.md")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasPrefix(string(skill), "---\nname: cronpad\ndescription: ") {
		t.Fatal("invalid skill frontmatter")
	}
	now := time.Unix(1800000000, 0)
	secret, e := webhook.GenerateSecret()
	if e != nil {
		t.Fatal(e)
	}
	type vector struct {
		Secret, ID, Timestamp, Signature, Body string
		Now                                    int64
		Want                                   bool
	}
	var good vector
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		good = vector{secret, r.Header.Get("webhook-id"), r.Header.Get("webhook-timestamp"), r.Header.Get("webhook-signature"), string(body), now.Unix(), true}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	cfg, _ := json.Marshal(webhook.Config{URL: receiver.URL})
	hook := webhook.New(netguard.Policy{AllowPrivateNetworks: true}, clockwork.NewFakeClockAt(now), "skill-test", 1024, time.Second)
	result := hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{ID: "job_skill", Name: "skill", Config: cfg, Timeout: time.Second, SigningSecrets: []sink.SigningSecret{{Value: secret}}}, RunID: "run_skill", ScheduledAt: now, Attempt: 1})
	if result.Outcome != sink.Success {
		t.Fatal(result)
	}
	vectors := []vector{good}
	bad := good
	bad.Body += "tamper"
	bad.Want = false
	vectors = append(vectors, bad)
	bad = good
	bad.Now += 301
	bad.Want = false
	vectors = append(vectors, bad)
	bad = good
	bad.Now -= 301
	bad.Want = false
	vectors = append(vectors, bad)
	bad = good
	bad.Signature = "v1,!!!"
	bad.Want = false
	vectors = append(vectors, bad)
	rotation := good
	rotation.Signature = "v1,!!! " + rotation.Signature
	vectors = append(vectors, rotation)
	bad = good
	bad.Secret = "whsec_!!!"
	bad.Want = false
	vectors = append(vectors, bad)
	bad = good
	bad.Timestamp = "invalid"
	bad.Want = false
	vectors = append(vectors, bad)
	data, _ := json.Marshal(vectors)
	for _, lang := range []string{"go", "javascript", "python"} {
		t.Run(lang, func(t *testing.T) {
			match := regexp.MustCompile("(?s)```" + lang + "\\n(.*?)\\n```").FindSubmatch(skill)
			if len(match) != 2 {
				t.Fatal("missing snippet")
			}
			dir := t.TempDir()
			var command *exec.Cmd
			switch lang {
			case "go":
				if e = os.WriteFile(filepath.Join(dir, "verify.go"), match[1], 0600); e != nil {
					t.Fatal(e)
				}
				harness := `package receiver
import("encoding/json";"os";"testing";"time")
func TestVectors(t *testing.T){var vectors []struct{Secret,ID,Timestamp,Signature,Body string;Now int64;Want bool};if e:=json.Unmarshal([]byte(os.Getenv("VECTORS")),&vectors);e!=nil{t.Fatal(e)};for i,v:=range vectors{if got:=Verify(v.Secret,v.ID,v.Timestamp,v.Signature,[]byte(v.Body),time.Unix(v.Now,0));got!=v.Want{t.Fatalf("vector %d: %v",i,got)}}}`
				if e = os.WriteFile(filepath.Join(dir, "verify_test.go"), []byte(harness), 0600); e != nil {
					t.Fatal(e)
				}
				command = exec.CommandContext(t.Context(), "go", "test", "verify.go", "verify_test.go")
			case "javascript":
				source := string(match[1]) + `\n`
				source = strings.TrimSuffix(source, `\n`) + "\n" + `for (const [i,v] of JSON.parse(process.env.VECTORS).entries()) {if(verify(v.Secret,v.ID,v.Timestamp,v.Signature,Buffer.from(v.Body),v.Now)!==v.Want) throw new Error('vector '+i);}`
				if e = os.WriteFile(filepath.Join(dir, "verify.cjs"), []byte(source), 0600); e != nil {
					t.Fatal(e)
				}
				command = exec.CommandContext(t.Context(), "node", "verify.cjs")
			case "python":
				source := string(match[1]) + "\nimport os, json\nfor i, v in enumerate(json.loads(os.environ['VECTORS'])):\n    assert verify(v['Secret'], v['ID'], v['Timestamp'], v['Signature'], v['Body'].encode(), v['Now']) == v['Want'], i\n"
				if e = os.WriteFile(filepath.Join(dir, "verify.py"), []byte(source), 0600); e != nil {
					t.Fatal(e)
				}
				command = exec.CommandContext(t.Context(), "python3", "verify.py")
			}
			command.Dir = dir
			command.Env = append(os.Environ(), "VECTORS="+string(data))
			output, e := command.CombinedOutput()
			if e != nil {
				t.Fatalf("%v: %s", e, output)
			}
		})
	}
}

func TestSkillScheduleExamples(t *testing.T) {
	// Verify every expression in the skill's syntax table with the production parser.
	skill, err := os.ReadFile("../../skills/cronpad/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	table := regexp.MustCompile(`(?s)\| Syntax \| Example \|\n\|---\|---\|\n(.*?)\n\n`).FindSubmatch(skill)
	if table == nil {
		t.Fatal("schedule syntax table not found")
	}
	exprs := regexp.MustCompile("(?m)^\\| [^|]+ \\| `([^`]+)`").FindAllSubmatch(table[1], -1)
	if len(exprs) < 7 {
		t.Fatalf("only %d schedule examples found", len(exprs))
	}
	now := clockwork.NewFakeClockAt(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC))
	for _, m := range exprs {
		expr := string(m[1])
		if _, err := schedule.ParseContext(context.Background(), schedule.Spec{Expr: expr, Timezone: "Asia/Kolkata"}, now); err != nil {
			t.Errorf("%s: %v", expr, err)
		}
	}
}

func TestSkillToolNamesMatchServer(t *testing.T) {
	// The skill must name every MCP tool, and only tools that exist.
	skill, err := os.ReadFile("../../skills/cronpad/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, d := range definitions() {
		known[d.name] = true
		if !strings.Contains(string(skill), "`"+d.name) {
			t.Errorf("skill never mentions the %s tool", d.name)
		}
	}
	for _, m := range regexp.MustCompile("`([a-z]+_(?:job|jobs|run|runs|schedule|stats))[`(]").FindAllSubmatch(skill, -1) {
		if name := string(m[1]); !known[name] && name != "debug_failing_job" {
			t.Errorf("skill names %s, which is not an MCP tool", name)
		}
	}
}
