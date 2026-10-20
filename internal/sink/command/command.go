// Package command executes isolated child process groups on self-hosted servers.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gowthamgts/cronpad/internal/config"
	"github.com/gowthamgts/cronpad/internal/sink"
)

type Config struct {
	Command          string            `json:"command"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	Workdir          string            `json:"workdir"`
	Shell            bool              `json:"shell"`
	SuccessExitCodes []int             `json:"success_exit_codes"`
	RetryExitCodes   []int             `json:"retry_exit_codes"`
}
type Command struct{ Config config.ConfigSinksCommand }

// New constructs a command sink; the caller controls registration.
func New(c config.ConfigSinksCommand) *Command { return &Command{Config: c} }

// Kind returns the sink discriminator.
func (*Command) Kind() string { return "command" }

// Validate rejects disabled commands, unsafe environment names and allowlist escapes.
func (c *Command) Validate(raw json.RawMessage, mode sink.Mode) error {
	if mode == sink.Hosted || !c.Config.Enabled {
		return fmt.Errorf("command sink is disabled on this server")
	}
	var in Config
	if err := json.Unmarshal(raw, &in); err != nil {
		return fmt.Errorf("command config: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" || strings.ContainsRune(in.Command, 0) {
		return fmt.Errorf("command is required")
	}
	if len(c.Config.AllowedCommands) > 0 && (in.Shell || !slices.Contains(c.Config.AllowedCommands, in.Command)) {
		return fmt.Errorf("command is not in allowed_commands; shell is forbidden with an allowlist")
	}
	// DECISION: executable and working-directory templates are forbidden so allowlist validation cannot be bypassed.
	if strings.Contains(in.Command, "{{") || strings.Contains(in.Workdir, "{{") {
		return fmt.Errorf("command and workdir cannot contain templates")
	}
	for name, value := range in.Env {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.HasPrefix(name, "CRONPAD_") {
			return fmt.Errorf("invalid environment name")
		}
		if _, err := sink.References(value); err != nil {
			return err
		}
	}
	for _, a := range in.Args {
		if _, err := sink.References(a); err != nil {
			return err
		}
	}
	return nil
}

type bounded struct {
	mu    sync.Mutex
	body  []byte
	limit int
}

func (b *bounded) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := max(0, b.limit-len(b.body))
	b.body = append(b.body, p[:min(left, n)]...)
	return n, nil
}

// Execute runs without a shell unless requested and kills descendants on cancellation.
func (c *Command) Execute(ctx context.Context, r sink.ExecRequest) sink.ExecResult {
	result := sink.ExecResult{Outcome: sink.Fatal, RequestSummary: "command"}
	if err := c.Validate(r.Job.Config, sink.SelfHosted); err != nil {
		result.Error = err.Error()
		return result
	}
	var in Config
	_ = json.Unmarshal(r.Job.Config, &in)
	var secretValues []string
	render := func(value string) (string, error) {
		v, s, e := sink.Render(ctx, value, r)
		secretValues = append(secretValues, s...)
		return v, e
	}
	args := make([]string, len(in.Args))
	for n, a := range in.Args {
		v, e := render(a)
		if e != nil {
			result.Error = e.Error()
			return result
		}
		args[n] = v
	}
	executable := in.Command
	if in.Shell {
		words := strings.Fields(c.Config.Shell)
		if len(words) == 0 {
			words = []string{"/bin/sh", "-c"}
		}
		if len(words) == 1 {
			words = append(words, "-c")
		}
		executable = words[0]
		args = append(append(words[1:], in.Command), args...)
	}
	if r.Job.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Job.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = in.Workdir
	env := map[string]string{}
	for _, name := range append([]string{"PATH", "HOME", "LANG"}, c.Config.EnvPassthrough...) {
		if value, ok := os.LookupEnv(name); ok {
			env[name] = value
		}
	}
	for name, value := range in.Env {
		v, e := render(value)
		if e != nil {
			result.Error = e.Error()
			return result
		}
		env[name] = v
	}
	env["CRONPAD_RUN_ID"] = r.RunID
	env["CRONPAD_JOB_ID"] = r.Job.ID
	env["CRONPAD_SCHEDULED_AT"] = r.ScheduledAt.UTC().Format(time.RFC3339Nano)
	env["CRONPAD_ATTEMPT"] = fmt.Sprint(r.Attempt)
	cmd.Env = make([]string, 0, len(env))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		e := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(e, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return e
	}
	cmd.WaitDelay = time.Second
	output := &bounded{limit: c.Config.MaxOutputBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	result.ResponseBody = sink.RedactBody(output.body, secretValues)
	if len(result.ResponseBody) > c.Config.MaxOutputBytes {
		result.ResponseBody = result.ResponseBody[:max(0, c.Config.MaxOutputBytes)]
	}
	if ctx.Err() != nil {
		result.Outcome = sink.Timeout
		result.Error = "command execution timed out or cancelled"
		return result
	}
	if cmd.ProcessState == nil {
		result.Error = "cannot start command"
		return result
	}
	result.Code = cmd.ProcessState.ExitCode()
	success := in.SuccessExitCodes
	if success == nil {
		success = []int{0}
	}
	switch {
	case slices.Contains(success, result.Code):
		result.Outcome = sink.Success
	case slices.Contains(in.RetryExitCodes, result.Code):
		result.Outcome = sink.Retryable
	default:
		result.Outcome = sink.Fatal
	}
	if err != nil {
		result.Error = fmt.Sprintf("command exited with code %d", result.Code)
	}
	return result
}
