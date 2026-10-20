package containerentrypoint

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonboulle/clockwork"
)

// Run in a subprocess because successful entrypoint intentionally replaces PID 1.
func TestEntrypointProcess(t *testing.T) {
	if os.Getenv("CRONPAD_ENTRYPOINT_HELPER") == "1" {
		err := Run(context.Background(), clockwork.NewRealClock(), slog.Default(), Logging{Format: os.Getenv("LOG_FORMAT"), Level: "info"}, []string{"serve"})
		if err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	for _, mode := range []string{"normal", "history-failure", "history-slow", "control-failure", "sqlite-only", "leftover-opt-out", "leftover-opt-out-replicated", "logfmt"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			events := filepath.Join(dir, "events")
			fake := `#!/bin/sh
 echo "$*" >> "$EVENTS"
 if [ "$1" = replicate ]; then echo "log=$LITESTREAM_LOG_TYPE/$LITESTREAM_LOG_LEVEL" >> "$EVENTS"; exit 0; fi
 case "$*" in
 *control.db*) if [ "$MODE" = control-failure ]; then exit 1; fi ;;
 *history.db*)
 if [ "$MODE" = history-failure ]; then exit 1; fi
 if [ "$MODE" = history-slow ]; then exec sleep 5; fi ;;
 esac
 while [ "$1" != '-o' ]; do shift; done
 shift
 echo restored > "$1"
 `
			for name, body := range map[string]string{"litestream": fake, "cronpad": "#!/bin/sh\necho direct >> \"$EVENTS\"\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			replica := "file://unused"
			// Only history-slow exercises the timeout; elsewhere a 10ms limit races the
			// fake restore's startup on a loaded machine and drops the history step.
			timeout := "10s"
			if mode == "history-slow" {
				timeout = "10ms"
			}
			env := []string{}
			switch mode {
			case "sqlite-only":
				replica = ""
			case "leftover-opt-out":
				replica = ""
				env = append(env, "LITESTREAM_DISABLED=true")
			case "leftover-opt-out-replicated":
				env = append(env, "LITESTREAM_DISABLED=true")
			}
			cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestEntrypointProcess$")
			cmd.Env = append(os.Environ(), "CRONPAD_ENTRYPOINT_HELPER=1", "PATH="+dir+":"+os.Getenv("PATH"), "EVENTS="+events, "MODE="+mode, "LITESTREAM_REPLICA_URL="+replica, "LITESTREAM_HISTORY_RESTORE_TIMEOUT="+timeout, "CRONPAD_DATABASE__PATH="+filepath.Join(dir, "control.db"), "CRONPAD_DATABASE__HISTORY_PATH="+filepath.Join(dir, "history.db"))
			if mode == "logfmt" {
				env = append(env, "LOG_FORMAT=logfmt")
			}
			cmd.Env = append(cmd.Env, env...)
			out, err := cmd.CombinedOutput()
			if mode == "control-failure" {
				if err == nil {
					t.Fatal("control restore must stop startup")
				}
			} else if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			b, _ := os.ReadFile(events)
			s := string(b)
			// Plain SQLite is the default, so nothing here is worth a warning; a leftover
			// LITESTREAM_DISABLED is only mentioned, and never stops replication.
			if (mode == "sqlite-only" || strings.HasPrefix(mode, "leftover-")) && strings.Contains(string(out), " WARN ") {
				t.Fatalf("unexpected warning: %s", out)
			}
			if leftover := strings.Contains(string(out), "LITESTREAM_DISABLED is no longer used"); leftover != strings.HasPrefix(mode, "leftover-") {
				t.Fatalf("leftover notice=%v for %s: %s", leftover, mode, out)
			}
			if mode == "sqlite-only" || mode == "leftover-opt-out" {
				if s != "direct\n" {
					t.Fatal(s)
				}
				if !strings.Contains(string(out), "running SQLite only; set LITESTREAM_REPLICA_URL to replicate") {
					t.Fatalf("missing SQLite-only notice: %s", out)
				}
				return
			}
			if !strings.Contains(s, "-if-db-not-exists -if-replica-exists") {
				t.Fatal(s)
			}
			if mode == "control-failure" {
				if strings.Contains(s, "replicate") {
					t.Fatal(s)
				}
				return
			}
			if strings.Index(s, "control.db") > strings.Index(s, "history.db") || !strings.Contains(s, "replicate") {
				t.Fatal(s)
			}
			// Litestream must log in cronpad's format: logfmt maps to Litestream's text handler.
			want := "log=json/info"
			if mode == "logfmt" {
				want = "log=text/info"
			}
			if !strings.Contains(s, want) {
				t.Fatalf("want %s in %s", want, s)
			}
			if strings.HasPrefix(mode, "history-") {
				if !strings.Contains(string(out), "starting with empty history") {
					t.Fatal(string(out))
				}
				if _, err = os.Stat(filepath.Join(dir, "history.db")); !os.IsNotExist(err) {
					t.Fatal("partial history installed")
				}
			}
		})
	}
}
