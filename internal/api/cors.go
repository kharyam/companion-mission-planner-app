package api

import (
	"net/http"
	"strings"
)

// corsMiddleware enforces the configured allowlist. Empty allowlist
// means CORS is effectively disabled (same-origin only). A "*" entry
// makes every Origin accepted — the request's Origin is echoed back
// because the CORS spec forbids a literal "*" alongside
// Allow-Credentials: true.
func corsMiddleware(allowed []string, next http.Handler) http.Handler {
	allowAny := false
	allowSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		if o == "*" {
			allowAny = true
			continue
		}
		allowSet[strings.ToLower(o)] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			_, listed := allowSet[strings.ToLower(origin)]
			if allowAny || listed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-KAM-Token")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			} else {
				// Unknown origin: still allow same-origin requests but tell
				// the browser this origin can't talk to us.
				w.Header().Set("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
