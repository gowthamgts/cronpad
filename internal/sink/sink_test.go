package sink

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTemplates(t *testing.T) {
	req := ExecRequest{Job: JobSnapshot{ID: "job_x", Name: "hello"}, RunID: "run_x", ScheduledAt: time.Unix(0, 0).UTC(), Attempt: 2, Secrets: SecretFunc(func(_ context.Context, n string) (string, error) { return "TOPSECRET", nil })}
	got, secrets, e := Render(context.Background(), `{{job.name}} {{job.id}} {{run.id}} {{run.scheduled_at}} {{attempt}} {{secret.TOKEN}}`, req)
	if e != nil || got != "hello job_x run_x 1970-01-01T00:00:00Z 2 TOPSECRET" {
		t.Fatal(got, e)
	}
	if strings.Contains(Redact(got, secrets), "TOPSECRET") {
		t.Fatal("secret leaked")
	}
	for _, s := range []string{`{{printf "%s" .}}`, `{{secret.foo}}`, `{{range .}}`, `{{job.unknown}}`, `{{`} {
		if _, e := References(s); e == nil {
			t.Fatal(s)
		}
	}
}
func TestRedactionFormsAndTruncatedBody(t *testing.T) {
	secret := "a b&c"
	for _, form := range []string{"a b&c", "a+b%26c", "a%20b&c"} {
		if got := Redact(form, []string{secret}); got != "[REDACTED]" {
			t.Fatal(got)
		}
	}
	if got := string(RedactBody([]byte("response:a b"), []string{secret})); got != "response:[REDACTED]" {
		t.Fatal(got)
	}
}
