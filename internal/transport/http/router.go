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

	"github.com/google/uuid"
	httpmw "github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/httpx"
	"github.com/sbezhuk/beebase-common/internalauth"
	mediahttp "github.com/sbezhuk/beebase-media-service/internal/transport/http/media"
)

// NewRouter builds the root HTTP handler for the service.
func NewRouter(
	log *slog.Logger,
	db *pgxpool.Pool,
	mediaHandler *mediahttp.Handler,
	tokenParser httpmw.AccessTokenParser,
	internalTokens ...string,
) http.Handler {
	internalToken := ""
	if len(internalTokens) > 0 {
		internalToken = internalTokens[0]
	}
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(requestLogger(log))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", HealthHandler)
	r.Get("/ready", ReadyHandler(db))
	r.With(internalauth.RequireAuth(internalToken)).Delete("/internal/api/v1/users/{userID}", func(w http.ResponseWriter, req *http.Request) {
		id, err := uuid.Parse(chi.URLParam(req, "userID"))
		if err != nil {
			httpx.WriteError(w, 400, "invalid_user_id", "invalid user id")
			return
		}
		if err := mediaHandler.DeleteUserData(req.Context(), id); err != nil {
			httpx.WriteError(w, 500, "cleanup_failed", "could not delete media")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	r.Route("/api/v1/media", func(r chi.Router) {
		r.Use(httpmw.RequireAuth(tokenParser))

		r.Post("/", mediaHandler.Upload)
		r.Get("/", mediaHandler.List)
		r.Get("/{mediaId}", mediaHandler.Get)
		r.Get("/{mediaId}/download", mediaHandler.Download)
		r.Delete("/{mediaId}", mediaHandler.Delete)
		// Internal-only: called by apiary-service/hive-service when they
		// delete an apiary/hive, to hard-delete every media id it knows it
		// references. This route group's RequireAuth can't distinguish that
		// from a genuine end-user call - beebase-gateway is what actually
		// blocks external reachability, by never proxying this exact
		// method+path.
		r.Delete("/", mediaHandler.DeleteByIDs)
		// Internal-only: called by auth-service when it deletes an account,
		// to sweep up every media item belonging to the caller (including
		// ones no apiary/hive/inspection ever referenced, e.g. the profile
		// avatar). Registered as a literal "/mine" segment - chi resolves a
		// static segment before a param one ({mediaID}), so this can never
		// be shadowed by the Delete("/{mediaID}", ...) route above.
		// beebase-gateway blocks external reachability the same way it does
		// for every other internal-only route in this project.
		r.Delete("/mine", mediaHandler.DeleteMine)
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
