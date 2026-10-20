// Package webhook implements signed HTTP delivery through the SSRF guard.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"

	"github.com/gowthamgts/cronpad/internal/netguard"
	"github.com/gowthamgts/cronpad/internal/outbound"
	"github.com/gowthamgts/cronpad/internal/sink"
)

type Config struct {
	URL             string            `json:"url"`
	Method          string            `json:"method"`
	Headers         map[string]string `json:"headers"`
	Body            *string           `json:"body"`
	SuccessCodes    json.RawMessage   `json:"success_codes"`
	RetryOn         []json.RawMessage `json:"retry_on"`
	FollowRedirects bool              `json:"follow_redirects"`
}
type Webhook struct {
	Hosts            *outbound.Hosts
	Policy           netguard.Policy
	Clock            clockwork.Clock
	UserAgent        string
	MaxResponseBytes int
	MaxTimeout       time.Duration
	transport        http.RoundTripper
	baseTransport    *http.Transport
}

// Option tunes outbound transport concurrency without changing delivery semantics.
type Option func(*http.Transport)

// WithConcurrency retains enough idle connections for a full worker burst.
func WithConcurrency(n int) Option {
	return func(t *http.Transport) {
		// DECISION: cap connections per destination to avoid a worker burst exhausting
		// the receiver SYN backlog or local ephemeral ports; reuse the entire pool.
		t.MaxConnsPerHost = min(128, max(1, n))
		t.MaxIdleConns = max(100, 2*n)
		t.MaxIdleConnsPerHost = t.MaxConnsPerHost
	}
}
func New(p netguard.Policy, c clockwork.Clock, ua string, maxBytes int, maxTimeout time.Duration, options ...Option) *Webhook {
	t := &http.Transport{DialContext: p.DialContext, MaxIdleConns: 100, MaxIdleConnsPerHost: 10, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: maxTimeout, ForceAttemptHTTP2: true}
	for _, option := range options {
		option(t)
	}
	return &Webhook{Policy: p, Clock: c, UserAgent: ua, MaxResponseBytes: maxBytes, MaxTimeout: maxTimeout, transport: otelhttp.NewTransport(t, otelhttp.WithPropagators(propagation.TraceContext{})), baseTransport: t}
}
func (w *Webhook) Kind() string { return "webhook" }
func decode(raw json.RawMessage) (Config, error) {
	var c Config
	e := json.Unmarshal(raw, &c)
	if c.Method == "" {
		c.Method = "POST"
	}
	if len(c.SuccessCodes) == 0 || string(c.SuccessCodes) == "null" {
		c.SuccessCodes = json.RawMessage(`"2xx"`)
	}
	if c.RetryOn == nil {
		c.RetryOn = []json.RawMessage{json.RawMessage(`"5xx"`), json.RawMessage(`408`), json.RawMessage(`429`), json.RawMessage(`"network"`), json.RawMessage(`"timeout"`)}
	}
	return c, e
}
func (w *Webhook) Validate(raw json.RawMessage, mode sink.Mode) error {
	c, e := decode(raw)
	if e != nil {
		return fmt.Errorf("webhook config: %w", e)
	}
	p := w.Policy
	p.Hosted = mode == sink.Hosted
	if e = p.CheckURL(c.URL); e != nil {
		return e
	}
	switch c.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("invalid webhook method")
	}
	if len(c.Headers) > 50 {
		return fmt.Errorf("at most 50 headers allowed")
	}
	texts := []string{c.URL}
	if c.Body != nil {
		texts = append(texts, *c.Body)
	}
	for k, v := range c.Headers {
		if !headerName(k) || strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid header")
		}
		if strings.HasPrefix(strings.ToLower(k), "cronpad-") {
			return fmt.Errorf("reserved header %s", k)
		}
		switch strings.ToLower(k) {
		case "host", "content-length", "connection", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-authenticate", "te", "trailer", "webhook-id", "webhook-timestamp", "webhook-signature", "idempotency-key", "cronpad-attempt", "cronpad-job-id", "cronpad-scheduled-at", "traceparent", "tracestate", "user-agent":
			return fmt.Errorf("reserved header %s", k)
		}
		texts = append(texts, v)
	}
	for _, s := range texts {
		if _, e = sink.References(s); e != nil {
			return e
		}
	}
	if !validSuccess(c.SuccessCodes) {
		return fmt.Errorf("invalid success_codes")
	}
	for _, v := range c.RetryOn {
		var s string
		var n int
		if json.Unmarshal(v, &s) == nil {
			if s != "5xx" && s != "4xx" && s != "network" && s != "timeout" {
				return fmt.Errorf("invalid retry_on")
			}
		} else if json.Unmarshal(v, &n) != nil || n < 100 || n > 599 {
			return fmt.Errorf("invalid retry_on")
		}
	}
	return nil
}
func headerName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		valid := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)
		if !valid {
			return false
		}
	}
	return true
}

// successTokens reads success_codes: "2xx" on its own (the original form) or a list mixing codes (301) and classes ("2xx").
func successTokens(raw []byte) ([]json.RawMessage, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []json.RawMessage{json.RawMessage(strconv.Quote(s))}, true
	}
	var vs []json.RawMessage
	if json.Unmarshal(raw, &vs) != nil || len(vs) == 0 {
		return nil, false
	}
	return vs, true
}

// classOf parses "1xx".."5xx" into its hundred.
func classOf(s string) (int, bool) {
	if len(s) == 3 && s[0] >= '1' && s[0] <= '5' && s[1:] == "xx" {
		return int(s[0]-'0') * 100, true
	}
	return 0, false
}

func validSuccess(raw []byte) bool {
	vs, ok := successTokens(raw)
	if !ok {
		return false
	}
	for _, v := range vs {
		var s string
		var n int
		if json.Unmarshal(v, &s) == nil {
			if _, ok := classOf(s); !ok {
				return false
			}
		} else if json.Unmarshal(v, &n) != nil || n < 100 || n > 599 {
			return false
		}
	}
	return true
}
func success(raw []byte, code int) bool {
	vs, _ := successTokens(raw)
	for _, v := range vs {
		var s string
		var n int
		if json.Unmarshal(v, &s) == nil {
			if base, ok := classOf(s); ok && code >= base && code < base+100 {
				return true
			}
		} else if json.Unmarshal(v, &n) == nil && n == code {
			return true
		}
	}
	return false
}
func retryMatch(vs []json.RawMessage, code int, kind string) bool {
	for _, v := range vs {
		var s string
		if json.Unmarshal(v, &s) == nil {
			if s == kind || s == "5xx" && code >= 500 || s == "4xx" && code >= 400 && code < 500 {
				return true
			}
		} else {
			var n int
			_ = json.Unmarshal(v, &n)
			if code != 0 && n == code {
				return true
			}
		}
	}
	return false
}
func GenerateSecret() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", fmt.Errorf("signing secret: %w", e)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(b), nil
}
func signatures(secrets []sink.SigningSecret, id, ts string, body []byte, now time.Time) (string, error) {
	out := []string{}
	for _, s := range secrets {
		if s.ExpiresAt != nil && !now.Before(*s.ExpiresAt) {
			continue
		}
		key, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(s.Value, "whsec_"))
		if e != nil || len(key) == 0 {
			return "", fmt.Errorf("invalid signing secret")
		}
		h := hmac.New(sha256.New, key)
		_, _ = h.Write([]byte(id + "." + ts + "."))
		_, _ = h.Write(body)
		out = append(out, "v1,"+base64.StdEncoding.EncodeToString(h.Sum(nil)))
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no active signing secret")
	}
	return strings.Join(out, " "), nil
}
func (w *Webhook) Execute(ctx context.Context, r sink.ExecRequest) (result sink.ExecResult) {
	timeout := r.Job.Timeout
	if timeout <= 0 || timeout > w.MaxTimeout {
		timeout = w.MaxTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result.Outcome = sink.Fatal
	secrets := []string{}
	defer func() {
		result.Error = sink.Redact(result.Error, secrets)
		result.RequestSummary = sink.Redact(result.RequestSummary, secrets)
		result.ResponseBody = sink.RedactBody(result.ResponseBody, secrets)
		if len(result.ResponseBody) > w.MaxResponseBytes {
			result.ResponseBody = result.ResponseBody[:w.MaxResponseBytes]
		}
	}()
	c, e := decode(r.Job.Config)
	if e != nil {
		result.Error = "invalid webhook config"
		return
	}
	if e = w.Validate(r.Job.Config, sink.Mode(func() string {
		if w.Policy.Hosted {
			return "hosted"
		}
		return "selfhosted"
	}())); e != nil {
		result.Error = e.Error()
		return
	}
	render := func(s string) (string, error) {
		v, ss, e := sink.Render(ctx, s, r)
		secrets = append(secrets, ss...)
		return v, e
	}
	url, e := render(c.URL)
	if e != nil {
		result.Error = e.Error()
		return
	}
	if e = w.Policy.CheckURL(url); e != nil {
		result.Error = e.Error()
		return
	}
	now := w.Clock.Now()
	var body []byte
	if c.Body != nil {
		v, err := render(*c.Body)
		if err != nil {
			result.Error = err.Error()
			return
		}
		body = []byte(v)
	} else {
		body, _ = json.Marshal(map[string]any{"run_id": r.RunID, "job_id": r.Job.ID, "job_name": r.Job.Name, "scheduled_at": r.ScheduledAt.UTC(), "attempt": r.Attempt, "fired_at": now.UTC()})
	}
	req, e := http.NewRequestWithContext(ctx, c.Method, url, strings.NewReader(string(body)))
	if e != nil {
		result.Error = e.Error()
		return
	}
	for k, v := range c.Headers {
		rendered, err := render(v)
		if err != nil {
			result.Error = err.Error()
			return
		}
		if strings.ContainsAny(rendered, "\r\n") {
			result.Error = "invalid rendered header"
			return
		}
		req.Header.Set(k, rendered)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	sig, e := signatures(r.Job.SigningSecrets, r.RunID, ts, body, now)
	if e != nil {
		result.Error = e.Error()
		return
	}
	req.Header.Set("webhook-id", r.RunID)
	req.Header.Set("webhook-timestamp", ts)
	req.Header.Set("webhook-signature", sig)
	req.Header.Set("Idempotency-Key", r.RunID)
	req.Header.Set("Cronpad-Attempt", strconv.Itoa(r.Attempt))
	req.Header.Set("Cronpad-Job-Id", r.Job.ID)
	req.Header.Set("Cronpad-Scheduled-At", r.ScheduledAt.UTC().Format(time.RFC3339Nano))
	req.Header.Set("User-Agent", w.UserAgent)
	result.RequestSummary = fmt.Sprintf("%s %s (%d bytes)", c.Method, url, len(body))
	client := &http.Client{Transport: limitedTransport{w.transport, w.Hosts}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if !c.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) > 5 {
			return fmt.Errorf("too many redirects")
		}
		if e := w.Policy.CheckURL(req.URL.String()); e != nil {
			return e
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
			for name := range c.Headers {
				req.Header.Del(name)
			}
		}

		redirectedBody := body
		if req.Body == nil {
			redirectedBody = nil
		}
		signature, err := signatures(r.Job.SigningSecrets, r.RunID, ts, redirectedBody, now)
		if err != nil {
			return err
		}
		req.Header.Set("webhook-signature", signature)
		return nil
	}}
	resp, e := client.Do(req)
	if e != nil {
		result.Error = e.Error()
		kind := "network"
		var ne net.Error
		if errors.Is(e, context.DeadlineExceeded) || errors.As(e, &ne) && ne.Timeout() {
			kind = "timeout"
		}
		if retryMatch(c.RetryOn, 0, kind) {
			result.Outcome = sink.Retryable
			if kind == "timeout" {
				result.Outcome = sink.Timeout
			}
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	result.Code = resp.StatusCode
	result.ResponseBody, e = io.ReadAll(io.LimitReader(resp.Body, int64(w.MaxResponseBytes)))
	if e != nil {
		result.Error = e.Error()
		result.Outcome = sink.Retryable
		return
	}
	if success(c.SuccessCodes, resp.StatusCode) {
		result.Outcome = sink.Success
	} else if retryMatch(c.RetryOn, resp.StatusCode, "") {
		result.Outcome = sink.Retryable
	}
	if resp.StatusCode == 429 || resp.StatusCode == 503 {
		result.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), now)
	}
	return
}
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v != "" && strings.IndexFunc(v, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		n, e := strconv.ParseUint(v, 10, 64)
		if e != nil || n > uint64(math.MaxInt64/int64(time.Second)) {
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(v); e == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// Close releases idle transport connections after all deliveries have stopped.
func (w *Webhook) Close() {
	if w.baseTransport != nil {
		w.baseTransport.CloseIdleConnections()
	}
}

type limitedTransport struct {
	next  http.RoundTripper
	hosts *outbound.Hosts
}

func (t limitedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.hosts.Wait(r.Context(), strings.ToLower(r.URL.Hostname())); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(r)
}
