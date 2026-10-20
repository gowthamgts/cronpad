package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/trace"

	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/sink"
)

// Independent verifier implemented directly from Standard Webhooks specification:
// sign message-id + '.' + unix timestamp + '.' + exact payload bytes, HMAC-SHA256,
// raw secret is base64-decoded after stripping whsec_, signatures are v1,<base64>.
// This intentionally shares no signing or decoding helpers with production code.
func verify(t *testing.T, secret string, h http.Header, body []byte, now time.Time) {
	t.Helper()
	key, e := base64.StdEncoding.DecodeString(secret[6:])
	if e != nil {
		t.Fatal(e)
	}
	seconds, err := strconv.ParseInt(h.Get("webhook-timestamp"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	age := now.Sub(time.Unix(seconds, 0))
	if age > 5*time.Minute || age < -5*time.Minute {
		t.Fatal("timestamp outside tolerance")
	}
	message := h.Get("webhook-id") + "." + h.Get("webhook-timestamp") + "." + string(body)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	for _, sig := range strings.Fields(h.Get("webhook-signature")) {
		parts := strings.SplitN(sig, ",", 2)
		if len(parts) == 2 && parts[0] == "v1" {
			provided, e := base64.StdEncoding.DecodeString(parts[1])
			if e == nil && hmac.Equal(provided, mac.Sum(nil)) {
				return
			}
		}
	}
	t.Fatal("no valid standard webhook signature")
}
func TestSignedDelivery(t *testing.T) {
	clk := clockwork.NewFakeClockAt(time.Unix(1700000000, 0))
	secret, _ := GenerateSecret()
	other, _ := GenerateSecret()
	expired, _ := GenerateSecret()
	past := clk.Now().Add(-time.Second)
	future := clk.Now().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verify(t, secret, r.Header, body, clk.Now())
		verify(t, other, r.Header, body, clk.Now())
		if len(strings.Fields(r.Header.Get("webhook-signature"))) != 2 {
			t.Error("rotation")
		}
		if r.Header.Get("Idempotency-Key") != "run_x" || r.Header.Get("webhook-id") != "run_x" || r.Header.Get("Cronpad-Attempt") != "2" || r.Header.Get("User-Agent") != "cronpad/test" || r.Header.Get("Traceparent") == "" {
			t.Error(r.Header)
		}
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil || payload["fired_at"] != clk.Now().UTC().Format(time.RFC3339) {
			t.Error(string(body))
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	hook := New(netguard.Policy{AllowPrivateNetworks: true}, clk, "cronpad/test", 1024, time.Second)
	cfg, _ := json.Marshal(Config{URL: srv.URL})
	req := sink.ExecRequest{Job: sink.JobSnapshot{ID: "job_x", Name: "test", Config: cfg, Timeout: time.Second, SigningSecrets: []sink.SigningSecret{{Value: secret}, {Value: other, ExpiresAt: &future}, {Value: expired, ExpiresAt: &past}}}, RunID: "run_x", ScheduledAt: clk.Now(), Attempt: 2}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled}))
	res := hook.Execute(ctx, req)
	if res.Outcome != sink.Success || res.Code != 202 {
		t.Fatal(res)
	}
}
func TestClassificationAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		code    int
		success string
		retry   []json.RawMessage
		want    sink.Outcome
	}{{200, "", nil, sink.Success}, {400, "", nil, sink.Fatal}, {503, "", nil, sink.Retryable}, {429, "", nil, sink.Retryable}, {418, `[418]`, nil, sink.Success}, {500, "", []json.RawMessage{}, sink.Fatal}} {
		t.Run(strconv.Itoa(tc.code)+string(tc.want), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer TOPSECRET" {
					t.Error("template")
				}
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte("TOPSECRET" + strings.Repeat("x", 100)))
			}))
			defer srv.Close()
			hook := New(netguard.Policy{AllowPrivateNetworks: true}, clockwork.NewRealClock(), "test", 16, time.Second)
			codes := json.RawMessage(tc.success)
			if len(codes) == 0 {
				codes = nil
			}
			cfg, _ := json.Marshal(Config{URL: srv.URL + "/?token={{secret.TOKEN}}", Headers: map[string]string{"Authorization": "Bearer {{secret.TOKEN}}"}, RetryOn: tc.retry, SuccessCodes: codes})
			res := hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{ID: "job_a", SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg}, RunID: "run_a", Secrets: sink.SecretFunc(func(context.Context, string) (string, error) { return "TOPSECRET", nil })})
			if res.Outcome != tc.want || strings.Contains(fmt.Sprint(res), "TOPSECRET") || len(res.ResponseBody) > 30 {
				t.Fatal(res)
			}
			if (tc.code == 429 || tc.code == 503) && res.RetryAfter != 30*time.Second {
				t.Fatal(res.RetryAfter)
			}
		})
	}
}
func TestRedirectGuardAndTimeout(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer srv.Close()
	hook := New(netguard.Policy{AllowPrivateNetworks: true}, clockwork.NewRealClock(), "test", 1024, time.Second)
	for _, follow := range []bool{false, true} {
		cfg, _ := json.Marshal(Config{URL: srv.URL, FollowRedirects: follow})
		res := hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg}})
		if follow && res.Code != 204 || !follow && res.Code != 302 {
			t.Fatal(res)
		}
	}
	if hook.Policy.CheckURL("http://127.0.0.1") != nil {
		t.Fatal("private override")
	}
	blocked := New(netguard.Policy{}, clockwork.NewRealClock(), "test", 1024, time.Second)
	cfg, _ := json.Marshal(Config{URL: srv.URL})
	if res := blocked.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg}}); res.Outcome != sink.Fatal {
		t.Fatal(res)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-clockwork.NewRealClock().After(50 * time.Millisecond):
		}
	}))
	defer slow.Close()
	cfg, _ = json.Marshal(Config{URL: slow.URL})
	res := hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg, Timeout: 10 * time.Millisecond}})
	if res.Outcome != sink.Timeout {
		t.Fatal(res)
	}
}
func TestRetryAfterDate(t *testing.T) {
	if got := parseRetryAfter("99999999999999999999999999", time.Time{}); got != time.Duration(1<<63-1) {
		t.Fatal(got)
	}
	now := time.Unix(1700000000, 0)
	if got := parseRetryAfter(now.Add(time.Minute).UTC().Format(http.TimeFormat), now); got != time.Minute {
		t.Fatal(got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRedirectLimitsAndPrivateHop(t *testing.T) {
	count := 0
	hook := New(netguard.Policy{}, clockwork.NewRealClock(), "test", 1024, time.Second)
	hook.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		count++
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"http://169.254.169.254/latest/meta-data"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	cfg, _ := json.Marshal(Config{URL: "https://public.example", FollowRedirects: true})
	res := hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg}})
	if count != 1 || !strings.Contains(res.Error, "not public") {
		t.Fatal(count, res)
	}
	count = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { count++; http.Redirect(w, r, "/again", http.StatusFound) }))
	defer srv.Close()
	hook = New(netguard.Policy{AllowPrivateNetworks: true}, clockwork.NewRealClock(), "test", 1024, time.Second)
	cfg, _ = json.Marshal(Config{URL: srv.URL, FollowRedirects: true})
	res = hook.Execute(context.Background(), sink.ExecRequest{Job: sink.JobSnapshot{SigningSecrets: []sink.SigningSecret{{Value: "whsec_dGVzdGluZy1rZXktMjQtYnl0ZXMtbG9uZw=="}}, Config: cfg}})
	if count != 6 || !strings.Contains(res.Error, "too many redirects") {
		t.Fatal(count, res)
	}
}

func TestSuccessCodesMixClassesAndCodes(t *testing.T) {
	cases := []struct {
		raw   string
		valid bool
		code  int
		ok    bool
	}{
		{`"2xx"`, true, 204, true},
		{`"2xx"`, true, 301, false},
		{`["2xx", 301, "4xx"]`, true, 301, true},
		{`["2xx", 301, "4xx"]`, true, 404, true},
		{`["2xx", 301, "4xx"]`, true, 302, false},
		{`[200, 202]`, true, 201, false},
		{`["6xx"]`, false, 0, false},
		{`["2XX"]`, false, 0, false},
		{`[]`, false, 0, false},
		{`[99]`, false, 0, false},
	}
	for _, c := range cases {
		if got := validSuccess([]byte(c.raw)); got != c.valid {
			t.Errorf("validSuccess(%s) = %v, want %v", c.raw, got, c.valid)
		}
		if c.valid && success([]byte(c.raw), c.code) != c.ok {
			t.Errorf("success(%s, %d) = %v, want %v", c.raw, c.code, !c.ok, c.ok)
		}
	}
}
