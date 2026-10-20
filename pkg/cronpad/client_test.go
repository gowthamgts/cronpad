package cronpad

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJobsOutputGolden(t *testing.T) {
	fixture := `{"data":[{"id":"job_123","name":"daily report","state":"active","schedule":{"expr":"0 9 * * *","timezone":"UTC"}}],"next_cursor":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cp_test" {
			t.Error("missing bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	for _, asJSON := range []bool{false, true} {
		t.Run(map[bool]string{false: "table", true: "json"}[asJSON], func(t *testing.T) {
			cmd := command()
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"jobs", "--url", server.URL, "--api-key", "cp_test", "list"}
			name := "jobs-table.golden"
			if asJSON {
				args = append(args, "--json")
				name = "jobs-json.golden"
			}
			cmd.SetArgs(args)
			if e := cmd.Execute(); e != nil {
				t.Fatal(e)
			}
			golden, e := os.ReadFile(filepath.Join("testdata", name))
			if e != nil {
				t.Fatal(e)
			}
			if out.String() != string(golden) {
				t.Fatalf("got:\n%swant:\n%s", out.String(), golden)
			}
		})
	}
}
func TestJobsCreateFlagsAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
				t.Error(e)
			}
			sink := body["sink"].(map[string]any)
			cfg := sink["config"].(map[string]any)
			if cfg["command"] != "/bin/echo" || cfg["args"].([]any)[0] != "hello world" {
				t.Error(body)
			}
			_, _ = w.Write([]byte(`{"id":"job_1","name":"test","state":"active","schedule":{"expr":"0 9 * * *","timezone":"UTC"}}`))
			return
		}
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"insufficient scope"}}`))
	}))
	defer server.Close()
	t.Setenv("CRONPAD_URL", server.URL)
	t.Setenv("CRONPAD_API_KEY", "env-key")
	cmd := command()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"jobs", "create", "--name", "test", "--schedule", "0 9 * * *", "--command", "/bin/echo", "--arg", "hello world", "--json"})
	if e := cmd.Execute(); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{{"jobs", "get", "job_1"}, {"jobs", "create", "--name", "test"}, {"jobs", "create", "-f", "missing.json", "--command", "/bin/echo"}} {
		cmd = command()
		cmd.SetArgs(args)
		if e := cmd.Execute(); e == nil {
			t.Fatal("expected error", args)
		} else if args[1] == "get" && e.Error() != "forbidden: insufficient scope" {
			t.Fatal(e)
		}
	}
}
func TestCLIErrorExitCode(t *testing.T) {
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCLIChild$")
	child.Env = append(os.Environ(), "CRONPAD_TEST_CLI_CHILD=1", "CRONPAD_URL=http://127.0.0.1:1", "CRONPAD_API_KEY=")
	output, e := child.CombinedOutput()
	if e == nil {
		t.Fatal("expected nonzero exit")
	}
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), "CRONPAD_API_KEY") {
		t.Fatalf("%v %s", e, output)
	}
}
func TestCLIChild(t *testing.T) {
	if os.Getenv("CRONPAD_TEST_CLI_CHILD") != "1" {
		return
	}
	os.Args = []string{"cronpad", "jobs", "get", "missing"}
	Main()
}
