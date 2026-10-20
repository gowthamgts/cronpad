// Package logx provides contextual structured logging and secret redaction.
package logx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type ctxKey struct{}

// With returns a child context with attributes; parents and siblings are immutable.
func With(ctx context.Context, args ...any) context.Context {
	r := slog.NewRecord(time.Time{}, 0, "", 0)
	r.Add(args...)
	attrs := Attrs(ctx)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	return context.WithValue(ctx, ctxKey{}, cloneAttrs(attrs))
}

// Attrs returns a copy of contextual attributes safe for the caller to change.
func Attrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return cloneAttrs(attrs)
}

// ContextHandler adds contextual attributes and active trace identifiers.
type ContextHandler struct{ next slog.Handler }

// NewContextHandler wraps a format handler.
func NewContextHandler(next slog.Handler) ContextHandler { return ContextHandler{next: next} }
func (h ContextHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone()
	r.AddAttrs(Attrs(ctx)...)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String(TraceID, sc.TraceID().String()), slog.String(SpanID, sc.SpanID().String()))
	}
	return h.next.Handle(ctx, r)
}
func (h ContextHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return ContextHandler{next: h.next.WithAttrs(a)}
}
func (h ContextHandler) WithGroup(g string) slog.Handler {
	return ContextHandler{next: h.next.WithGroup(g)}
}

// Secret always renders as a redacted value in structured logs.
type Secret string

func (Secret) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

var sensitive = regexp.MustCompile(`(?i)authorization|cookie|token|secret|password|api_key|signature`)

// ReplaceAttr redacts sensitive attribute names at any group depth.
func ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	for _, group := range groups {
		if sensitive.MatchString(group) {
			a.Value = slog.StringValue("[REDACTED]")
			return a
		}
	}
	if sensitive.MatchString(a.Key) {
		a.Value = slog.StringValue("[REDACTED]")
	}
	return a
}

// Config selects the format, level, source location, and fixed service metadata.
type Config struct {
	Level, Format, Service, Version string
	AddSource                       bool
}

// Setup builds a logger. Bridge is an optional OTel log handler using the same redaction.
// Setup also installs the process default, as required for context-taking slog calls.
func Setup(cfg Config, out io.Writer, bridge slog.Handler) *slog.Logger {
	if cfg.Service == "" {
		cfg.Service = "cronpad"
	}
	if cfg.Version == "" {
		cfg.Version = "unknown"
	}
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Level))
	opts := &slog.HandlerOptions{Level: level, AddSource: cfg.AddSource, ReplaceAttr: ReplaceAttr}
	var h slog.Handler = slog.NewJSONHandler(out, opts)
	if cfg.Format == "logfmt" {
		h = slog.NewTextHandler(out, opts)
	}
	if bridge != nil {
		h = fanout{h, redactingHandler{next: bridge, level: level}}
	}
	logger := slog.New(NewContextHandler(h)).With(Service, cfg.Service, Version, cfg.Version)
	slog.SetDefault(logger)
	return logger
}

type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}
func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}
func (f fanout) WithAttrs(a []slog.Attr) slog.Handler {
	n := make(fanout, len(f))
	for i, h := range f {
		n[i] = h.WithAttrs(a)
	}
	return n
}
func (f fanout) WithGroup(g string) slog.Handler {
	n := make(fanout, len(f))
	for i, h := range f {
		n[i] = h.WithGroup(g)
	}
	return n
}

// cloneAttrs also isolates nested groups from callers of Attrs.
func cloneAttrs(attrs []slog.Attr) []slog.Attr {
	result := slices.Clone(attrs)
	for i, a := range result {
		if a.Value.Kind() == slog.KindGroup {
			result[i] = slog.Group(a.Key, attrsToAny(cloneAttrs(a.Value.Group()))...)
		}
	}
	return result
}
func attrsToAny(attrs []slog.Attr) []any {
	out := make([]any, len(attrs))
	for i, a := range attrs {
		out[i] = a
	}
	return out
}

// redactingHandler protects the OTLP bridge, which does not accept ReplaceAttr.
type redactingHandler struct {
	next  slog.Handler
	level slog.Level
}

func (h redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level && h.next.Enabled(ctx, l)
}
func redact(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	a = ReplaceAttr(nil, a)
	if a.Value.Kind() == slog.KindGroup {
		attrs := a.Value.Group()
		safe := make([]any, len(attrs))
		for i, v := range attrs {
			safe[i] = redact(v)
		}
		return slog.Group(a.Key, safe...)
	}
	return a
}
func (h redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	safe := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	r.Attrs(func(a slog.Attr) bool { safe.AddAttrs(redact(a)); return true })
	return h.next.Handle(ctx, safe)
}
func (h redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		safe[i] = redact(a)
	}
	return redactingHandler{next: h.next.WithAttrs(safe), level: h.level}
}
func (h redactingHandler) WithGroup(g string) slog.Handler {
	return redactingHandler{next: h.next.WithGroup(g), level: h.level}
}
