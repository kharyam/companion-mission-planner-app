package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/api/web"
	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/device"
)

// Server is the local HTTP API that KAM Mission Planner consumes.
type Server struct {
	cfg      *config.Config
	registry *device.Registry
	logger   *slog.Logger

	subsMu sync.RWMutex
	subs   map[*subscriber]struct{}
}

// Event is broadcast over the WebSocket. Keep the schema stable; KAM
// matches on Type.
type Event struct {
	Type   string    `json:"type"`
	Device string    `json:"deviceId,omitempty"`
	Slot   string    `json:"slot,omitempty"`
	At     time.Time `json:"at"`
	Detail any       `json:"detail,omitempty"`
}

type subscriber struct {
	send chan Event
}

func New(cfg *config.Config, reg *device.Registry, logger *slog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		registry: reg,
		logger:   logger,
		subs:     map[*subscriber]struct{}{},
	}
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	s.routes(mux)

	// CORS must wrap auth: a browser preflight (OPTIONS) carries no
	// credentials, so if auth ran first it would 401 the preflight
	// without CORS headers and the browser would report a generic
	// "NetworkError when attempting to fetch resource". Putting CORS
	// outermost also means auth's own 401 responses still carry the
	// allow-origin header, so the planner can read the error body.
	handler := corsMiddleware(s.cfg.Server.CORSOrigins, s.authMiddleware(mux))

	addr := net.JoinHostPort(s.cfg.Server.Bind, strconv.Itoa(s.cfg.Server.Port))
	hs := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  s.cfg.Server.ReadTimeout.Std(),
		WriteTimeout: s.cfg.Server.WriteTimeout.Std(),
	}

	// Fan registry-level device events out to WebSocket subscribers.
	go s.pumpDeviceEvents(ctx)

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("listening", "addr", addr)
		errCh <- hs.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// pumpDeviceEvents subscribes to registry events and rebroadcasts them
// as API-level WebSocket events. Backs off and retries if the underlying
// goadb watcher dies (e.g. adb-server restart).
func (s *Server) pumpDeviceEvents(ctx context.Context) {
	const backoff = 5 * time.Second
	for ctx.Err() == nil {
		ch := s.registry.Watch(ctx)
		for ev := range ch {
			s.broadcast(Event{Type: ev.Type, Device: ev.DeviceID, At: ev.At})
		}
		// Channel closed; either ctx cancelled or watcher errored.
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// routes wires the URL surface. We use Go 1.22's pattern-based ServeMux,
// which supports method matching and {placeholder} segments natively —
// no third-party router needed.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/devices", s.handleListDevices)
	mux.HandleFunc("GET /api/devices/{deviceId}/slots", s.handleListSlots)
	mux.HandleFunc("GET /api/devices/{deviceId}/slots/{guid}/preview", s.handleReadPreview)
	mux.HandleFunc("GET /api/devices/{deviceId}/slots/{guid}/kmz", s.handleDownloadKMZ)
	mux.HandleFunc("POST /api/devices/{deviceId}/slots/{guid}/transfer", s.handleTransfer)
	mux.HandleFunc("DELETE /api/devices/{deviceId}/slots/{guid}", s.handleClearSlot)
	mux.HandleFunc("POST /api/devices/{deviceId}/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/kmz/inspect", s.handleInspectKMZ)
	mux.HandleFunc("PUT /api/devices/{deviceId}/slots/{guid}/name", s.handleSetSlotName)
	mux.HandleFunc("DELETE /api/devices/{deviceId}/slots/{guid}/name", s.handleClearSlotName)
	mux.HandleFunc("PUT /api/devices/{deviceId}/slots/{guid}/managed", s.handleSetSlotManaged)
	mux.HandleFunc("POST /api/devices/{deviceId}/slots/{guid}/preview/regenerate", s.handleRegeneratePreview)
	mux.HandleFunc("POST /api/devices/{deviceId}/slots/{guid}/waypoint-images", s.handlePushWaypointImages)
	mux.HandleFunc("PUT /api/devices/{deviceId}/slot-order", s.handleSetSlotOrder)
	mux.HandleFunc("GET /api/events", s.handleEvents)

	// Admin UI
	staticFS, err := web.StaticFS()
	if err == nil {
		fileServer := http.FileServer(http.FS(staticFS))
		mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", fileServer))
		mux.HandleFunc("GET /ui/", s.handleUIIndex)
		mux.HandleFunc("GET /ui", s.handleUIIndex)
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	} else {
		s.logger.Warn("admin UI assets unavailable", "err", err)
	}
}

// handleUIIndex serves the SPA shell. We read it on demand rather than
// caching the bytes so a `go run ./cmd/kam-transfer` during development
// picks up edits without a rebuild — go:embed still wins at release time.
func (s *Server) handleUIIndex(w http.ResponseWriter, r *http.Request) {
	staticFS, err := web.StaticFS()
	if err != nil {
		http.Error(w, "ui unavailable", http.StatusInternalServerError)
		return
	}
	f, err := staticFS.Open("index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, f)
}

// authMiddleware enforces the optional bearer token from config.
// Empty token disables auth entirely (intended for local dev).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	token := s.cfg.Auth.Token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Accept either Authorization: Bearer <token> or X-KAM-Token: <token>.
		got := r.Header.Get("X-KAM-Token")
		if got == "" {
			h := r.Header.Get("Authorization")
			const p = "Bearer "
			if len(h) > len(p) && h[:len(p)] == p {
				got = h[len(p):]
			}
		}
		if got != token {
			writeError(w, http.StatusUnauthorized, CodeUnauthorized, "missing or invalid auth token", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// broadcast fans an event out to all WebSocket subscribers. Drops to
// slow subscribers rather than blocking the server.
func (s *Server) broadcast(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for sub := range s.subs {
		select {
		case sub.send <- ev:
		default:
			// drop; we don't want one slow consumer to wedge the API
		}
	}
}

// Address returns the bound address (useful for tests).
func (s *Server) Address() string {
	return fmt.Sprintf("%s:%d", s.cfg.Server.Bind, s.cfg.Server.Port)
}
