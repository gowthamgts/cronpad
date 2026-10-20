// Package sink defines delivery contracts and restricted, secret-aware templates.
package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"
)

type Mode string

const (
	SelfHosted Mode = "selfhosted"
	Hosted     Mode = "hosted"
)

type Outcome string

const (
	Success   Outcome = "success"
	Retryable Outcome = "retryable"
	Fatal     Outcome = "fatal"
	Timeout   Outcome = "timeout"
)

type Sink interface {
	Kind() string
	Validate(json.RawMessage, Mode) error
	Execute(context.Context, ExecRequest) ExecResult
}
type SecretResolver interface {
	Resolve(context.Context, string) (string, error)
}
type SecretFunc func(context.Context, string) (string, error)

func (f SecretFunc) Resolve(ctx context.Context, n string) (string, error) { return f(ctx, n) }

type SigningSecret struct {
	Value     string     `json:"value"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type JobSnapshot struct {
	ID, Name, WorkspaceID string
	Config                json.RawMessage
	Timeout               time.Duration
	SigningSecrets        []SigningSecret
}
type ExecRequest struct {
	Job            JobSnapshot
	RunID          string
	ScheduledAt    time.Time
	Attempt        int
	IdempotencyKey string
	Secrets        SecretResolver
}
type ExecResult struct {
	Outcome        Outcome
	Code           int
	Error          string
	ResponseBody   []byte
	RequestSummary string
	RetryAfter     time.Duration
}

var token = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)
var secretName = regexp.MustCompile(`^secret\.([A-Z][A-Z0-9_]*)$`)

// References rejects all template actions except the documented placeholders.
func References(s string) ([]string, error) {
	names := []string{}
	remaining := token.ReplaceAllStringFunc(s, func(raw string) string { return "" })
	if strings.Contains(remaining, "{{") || strings.Contains(remaining, "}}") {
		return nil, fmt.Errorf("invalid template")
	}
	for _, m := range token.FindAllStringSubmatch(s, -1) {
		v := strings.TrimSpace(m[1])
		if n := secretName.FindStringSubmatch(v); n != nil {
			names = append(names, n[1])
			continue
		}
		switch v {
		case "run.id", "run.scheduled_at", "job.id", "job.name", "attempt":
		default:
			return nil, fmt.Errorf("unsupported template placeholder %q", v)
		}
	}
	return names, nil
}

// Render compiles only whitelisted actions into a text/template with no user funcs.
func Render(ctx context.Context, s string, r ExecRequest) (string, []string, error) {
	if _, e := References(s); e != nil {
		return "", nil, e
	}
	values := map[string]string{"run.id": r.RunID, "run.scheduled_at": r.ScheduledAt.UTC().Format(time.RFC3339Nano), "job.id": r.Job.ID, "job.name": r.Job.Name, "attempt": fmt.Sprint(r.Attempt)}
	secrets := []string{}
	var err error
	source := token.ReplaceAllStringFunc(s, func(raw string) string {
		v := strings.TrimSpace(token.FindStringSubmatch(raw)[1])
		if n := secretName.FindStringSubmatch(v); n != nil {
			if r.Secrets == nil {
				err = fmt.Errorf("unknown secret %s", n[1])
				return ""
			}
			value, e := r.Secrets.Resolve(ctx, n[1])
			if e != nil {
				err = fmt.Errorf("cannot resolve secret %s", n[1])
				return ""
			}
			values[v] = value
			secrets = append(secrets, value)
		}
		return "{{index . " + fmt.Sprintf("%q", v) + "}}"
	})
	if err != nil {
		return "", secrets, err
	}
	t, e := template.New("sink").Option("missingkey=error").Parse(source)
	if e != nil {
		return "", secrets, fmt.Errorf("template: %w", e)
	}
	var b bytes.Buffer
	if e = t.Execute(&b, values); e != nil {
		return "", secrets, fmt.Errorf("render: %w", e)
	}
	return b.String(), secrets, nil
}
func Redact(s string, secrets []string) string {
	variants := []string{}
	for _, v := range secrets {
		if v != "" {
			b, _ := json.Marshal(v)
			variants = append(variants, v, url.QueryEscape(v), url.PathEscape(v), string(b[1:len(b)-1]))
		}
	}
	sort.Slice(variants, func(i, j int) bool { return len(variants[i]) > len(variants[j]) })
	for _, v := range variants {
		s = strings.ReplaceAll(s, v, "[REDACTED]")
	}
	return s
}

// RedactBody also removes a secret prefix cut off by the response size limit.
func RedactBody(body []byte, secrets []string) []byte {
	s := string(body)
	for _, v := range secrets {
		if v == "" {
			continue
		}
		for n := min(len(s), len(v)-1); n > 0; n-- {
			if strings.HasSuffix(s, v[:n]) {
				s = s[:len(s)-n] + "[REDACTED]"
				break
			}
		}
	}
	return []byte(Redact(s, secrets))
}
