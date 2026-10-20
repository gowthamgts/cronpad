package app

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jonboulle/clockwork"

	"github.com/gowthamgts/cronpad/internal/logx"
)

// requestLogging sits outside Recoverer so recovered panics are logged as 500s.
// Only route templates are logged: raw paths and query strings can contain secrets.
func requestLogging(log *slog.Logger, clk clockwork.Clock) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := logx.With(r.Context(), logx.RequestID, middleware.GetReqID(r.Context()), logx.Method, r.Method)
			r = r.WithContext(ctx)
			start := clk.Now()
			response := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(response, r)
			status := response.Status()
			if status == 0 {
				status = http.StatusOK
			}
			route := chi.RouteContext(ctx).RoutePattern()
			if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || route == "" {
				route = "<unmatched>"
			}
			log.InfoContext(ctx, "request finished", logx.Route, route, logx.StatusCode, status, logx.DurationMS, clk.Since(start).Milliseconds())
		})
	}
}
