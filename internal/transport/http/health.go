package http

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sbezhuk/beebase-common/httpx"
)

type statusResponse struct {
	Status string `json:"status"`
}

// HealthHandler reports liveness: the process is up and serving requests.
// It has no external dependencies and should never fail while the process
// is running, which is what makes it suitable for a container liveness probe.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

// ReadyHandler reports readiness: the process is up and able to serve
// traffic, i.e. its dependencies (the database) are reachable. Suitable
// for a container/load-balancer readiness probe.
func ReadyHandler(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			httpx.WriteJSON(w, http.StatusServiceUnavailable, statusResponse{Status: "unavailable"})
			return
		}

		httpx.WriteJSON(w, http.StatusOK, statusResponse{Status: "ok"})
	}
}
