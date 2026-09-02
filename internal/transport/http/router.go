// Package http wires the HTTP transport: routing, middleware, and the
// handlers that don't yet belong to a specific domain (health, readiness).
package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	httpmw "github.com/sbezhuk/beebase-common/authmw"
	mediahttp "github.com/sbezhuk/beebase-media-service/internal/transport/http/media"
)

// NewRouter builds the root HTTP handler for the service.
func NewRouter(
	log *slog.Logger,
	db *pgxpool.Pool,
	mediaHandler *mediahttp.Handler,
	tokenParser httpmw.AccessTokenParser,
) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", HealthHandler)
	r.Get("/ready", ReadyHandler(db))

	r.Route("/api/v1/media", func(r chi.Router) {
		r.Use(httpmw.RequireAuth(tokenParser))

		r.Post("/", mediaHandler.Upload)
		r.Get("/", mediaHandler.List)
		r.Get("/{mediaID}", mediaHandler.Get)
		r.Get("/{mediaID}/download", mediaHandler.Download)
		r.Post("/{mediaID}/attach", mediaHandler.Attach)
		r.Delete("/{mediaID}", mediaHandler.Delete)
		// Internal cascade primitive: called by apiary-service/hive-service
		// when they delete an apiary/hive, forwarding the caller's own
		// access token.
		r.Delete("/", mediaHandler.DeleteByOwner)
	})

	return r
}

// requestLogger logs each request's method, path, status, and duration
// through slog instead of chi's default stdlib logger.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			log.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
