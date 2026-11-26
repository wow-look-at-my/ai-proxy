// updater.go holds updaterPrefix.

package relay

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// updaterPrefix is where docker-updater asks its update checks.
const updaterPrefix = "/.well-known/docker-updater/"

// reservedRoute is the route name that would hide updaterPrefix.
const reservedRoute = ".well-known"

func (h *Handler) updater(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimPrefix(r.URL.Path, updaterPrefix) {
	case "health":
		// docker-updater rejects an answer from a process older than the new container.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"started": h.started.Format(time.RFC3339Nano)})
	case "pre-update":
		if n := h.inflight.Load(); n > 0 {
			h.log.Info("update held", "in_flight", n)
			writeError(w, http.StatusServiceUnavailable, "relay_busy", "requests are in flight; ask again later")
			return
		}
		h.log.Info("update allowed")
		w.WriteHeader(http.StatusOK)
	default:
		writeError(w, http.StatusNotFound, "relay_no_route", "no route for "+r.URL.Path)
	}
}
