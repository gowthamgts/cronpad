// Package containerentrypoint supervises restore before replacing PID 1 with Litestream.
package containerentrypoint

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/logx"
)

// Logging is cronpad's configured log format ("json" or "logfmt") and level.
type Logging struct{ Format, Level string }

// DECISION: use the stable CLI. Litestream explicitly documents its Go library
// API as unstable: https://litestream.io/guides/go-library/ .
// DECISION: a hidden Go subcommand keeps the entrypoint independent of any shell in the image.
func Run(ctx context.Context, clk clockwork.Clock, log *slog.Logger, logging Logging, args []string) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if args[0] != "serve" {
		return replace("cronpad", args)
	}
	// DECISION: plain SQLite is the default; replication is opt-in with LITESTREAM_REPLICA_URL.
	// The switch is a LITESTREAM_* variable, not CRONPAD_*: every CRONPAD_* variable is parsed
	// as a config key, and Litestream runs outside the cronpad process.
	if os.Getenv("LITESTREAM_DISABLED") != "" {
		// Retired opt-out from when an unset replica URL was a warning.
		log.InfoContext(ctx, "LITESTREAM_DISABLED is no longer used and is ignored; plain SQLite is the default")
	}
	if os.Getenv("LITESTREAM_REPLICA_URL") == "" {
		log.InfoContext(ctx, "running SQLite only; set LITESTREAM_REPLICA_URL to replicate")
		return replace("cronpad", args)
	}
	config := os.Getenv("LITESTREAM_CONFIG")
	if config == "" {
		config = "/etc/litestream.yml"
	}
	// DECISION: Litestream shares the container's log stream, so it logs in cronpad's
	// format and level; deploy/litestream.yml reads these two variables.
	litestreamType := "json"
	if logging.Format == "logfmt" {
		litestreamType = "text"
	}
	for k, v := range map[string]string{"LITESTREAM_LOG_TYPE": litestreamType, "LITESTREAM_LOG_LEVEL": logging.Level} {
		if err := os.Setenv(k, v); err != nil {
			return err
		}
	}
	control := os.Getenv("CRONPAD_DATABASE__PATH")
	if control == "" {
		control = "/data/cronpad.db"
		if err := os.Setenv("CRONPAD_DATABASE__PATH", control); err != nil {
			return err
		}
	}
	history := os.Getenv("CRONPAD_DATABASE__HISTORY_PATH")
	if history == "" {
		history = "/data/history.db"
		if err := os.Setenv("CRONPAD_DATABASE__HISTORY_PATH", history); err != nil {
			return err
		}
	}
	if err := restore(ctx, config, control); err != nil {
		return fmt.Errorf("restore control: %w", err)
	}
	if _, err := os.Stat(history); errors.Is(err, os.ErrNotExist) {
		timeout := 5 * time.Second
		if raw := os.Getenv("LITESTREAM_HISTORY_RESTORE_TIMEOUT"); raw != "" {
			var e error
			timeout, e = time.ParseDuration(raw)
			if e != nil || timeout <= 0 {
				return fmt.Errorf("invalid history restore timeout %q", raw)
			}
		}
		restoreCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- restore(restoreCtx, config, history) }()
		timer := clk.NewTimer(timeout)
		var e error
		select {
		case e = <-done:
		case <-timer.Chan():
			cancel()
			e = <-done
			if e == nil {
				e = context.DeadlineExceeded
			}
		case <-ctx.Done():
			cancel()
			<-done
			timer.Stop()
			return ctx.Err()
		}
		timer.Stop()
		cancel()
		if e != nil {
			// The only file restored was the scratch output; a partial history is never opened.
			log.ErrorContext(ctx, "history restore failed or timed out; starting with empty history", logx.Error, e)
		} else if _, e = os.Stat(history); errors.Is(e, os.ErrNotExist) {
			log.ErrorContext(ctx, "history replica missing; starting with empty history")
		}
	} else if err != nil {
		return fmt.Errorf("inspect history: %w", err)
	}
	command := []string{"cronpad"}
	for _, arg := range args {
		command = append(command, shellQuote(arg))
	}
	return replace("litestream", []string{"replicate", "-config", config, "-exec", strings.Join(command, " ")})
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func restore(ctx context.Context, config, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("restore directory: %w", err)
	}
	tmp := path + ".restore-" + strconv.Itoa(os.Getpid())
	defer func() {
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(tmp + suffix)
		}
	}()
	cmd := exec.CommandContext(ctx, "litestream", "restore", "-config", config, "-if-db-not-exists", "-if-replica-exists", "-o", tmp, path)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("litestream restore: %w", err)
	}
	if _, err := os.Stat(tmp); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("restore output: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install restore: %w", err)
	}
	return nil
}
func replace(program string, args []string) error {
	path, err := exec.LookPath(program)
	if err != nil {
		return fmt.Errorf("find %s: %w", program, err)
	}
	if err = syscall.Exec(path, append([]string{program}, args...), os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", program, err)
	}
	return nil
}
