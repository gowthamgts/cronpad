package command_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/sink"
	"github.com/gowthamgts/cronpad/internal/sink/command"
)

func execute(t *testing.T, c *command.Command, in command.Config, timeout time.Duration) sink.ExecResult {
	t.Helper()
	raw, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	return c.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{ID: "job_test", Config: raw, Timeout: timeout}, RunID: "run_test", Attempt: 2, ScheduledAt: clockwork.NewRealClock().Now(), Secrets: sink.SecretFunc(func(context.Context, string) (string, error) { return "topsecret", nil })})
}
func TestExitCodeClassification(t *testing.T) {
	c := command.New(config.ConfigSinksCommand{Enabled: true, MaxOutputBytes: 1024})
	for _, tc := range []struct {
		code string
		want sink.Outcome
	}{{"0", sink.Success}, {"75", sink.Retryable}, {"3", sink.Fatal}, {"7", sink.Success}} {
		t.Run(tc.code, func(t *testing.T) {
			r := execute(t, c, command.Config{Command: "/bin/sh", Args: []string{"-c", "exit " + tc.code}, SuccessExitCodes: []int{0, 7}, RetryExitCodes: []int{75}}, time.Second)
			if r.Outcome != tc.want {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestEnvironmentIsolationTemplatesRedactionAndOutputBound(t *testing.T) {
	t.Setenv("PARENT_SECRET", "never-inherit")
	t.Setenv("PASSTHROUGH", "allowed")
	c := command.New(config.ConfigSinksCommand{Enabled: true, MaxOutputBytes: 16384, EnvPassthrough: []string{"PASSTHROUGH"}})
	r := execute(t, c, command.Config{Command: "/usr/bin/env", Env: map[string]string{"TOKEN": "{{secret.TOKEN}}", "JOB": "{{job.id}}"}}, time.Second)
	body := string(r.ResponseBody)
	for _, want := range []string{"CRONPAD_RUN_ID=run_test", "CRONPAD_ATTEMPT=2", "PASSTHROUGH=allowed", "TOKEN=[REDACTED]", "JOB=job_test"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	if strings.Contains(body, "PARENT_SECRET") || strings.Contains(body, "topsecret") {
		t.Fatal(body)
	}
	c.Config.MaxOutputBytes = 16
	r = execute(t, c, command.Config{Command: "/bin/sh", Args: []string{"-c", "printf 123456789012345678901234567890; printf error >&2"}}, time.Second)
	if len(r.ResponseBody) != 16 {
		t.Fatal(len(r.ResponseBody))
	}
}
func TestTimeoutKillsGrandchildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "survived")
	c := command.New(config.ConfigSinksCommand{Enabled: true, MaxOutputBytes: 1024})
	r := execute(t, c, command.Config{Command: "/bin/sh", Args: []string{"-c", "(sleep 0.3; touch \"$1\") & wait", "--", marker}}, 50*time.Millisecond)
	if r.Outcome != sink.Timeout {
		t.Fatalf("%+v", r)
	}
	<-clockwork.NewRealClock().After(400 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("grandchild survived: %v", err)
	}
}
func TestHostedModeAndDisabledCommandRejected(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		mode    sink.Mode
	}{{true, sink.Hosted}, {false, sink.SelfHosted}} {
		c := command.New(config.ConfigSinksCommand{Enabled: tc.enabled})
		if c.Validate(json.RawMessage(`{"command":"/bin/echo"}`), tc.mode) == nil {
			t.Fatal("command accepted")
		}
	}
}
func TestAllowlistAndNoShellByDefault(t *testing.T) {
	c := command.New(config.ConfigSinksCommand{Enabled: true, AllowedCommands: []string{"/bin/echo"}, MaxOutputBytes: 1024})
	for _, raw := range []string{`{"command":"/bin/sh"}`, `{"command":"/bin/echo","shell":true}`, `{"command":"{{secret.CMD}}"}`} {
		if c.Validate(json.RawMessage(raw), sink.SelfHosted) == nil {
			t.Fatal(raw)
		}
	}
	r := execute(t, c, command.Config{Command: "/bin/echo", Args: []string{"$HOME; echo unsafe"}}, time.Second)
	if string(r.ResponseBody) != "$HOME; echo unsafe\n" {
		t.Fatalf("%q", r.ResponseBody)
	}
}

func TestConfiguredShell(t *testing.T) {
	c := command.New(config.ConfigSinksCommand{Enabled: true, Shell: "/bin/sh", MaxOutputBytes: 1024})
	r := execute(t, c, command.Config{Command: "printf hello | tr a-z A-Z", Shell: true}, time.Second)
	if r.Outcome != sink.Success || string(r.ResponseBody) != "HELLO" {
		t.Fatalf("%+v", r)
	}
}
