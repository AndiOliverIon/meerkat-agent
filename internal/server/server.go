// Package server exposes the agent snapshot over HTTPS: TLS with the agent's
// self-signed cert, and a per-install bearer token on the sensitive endpoint.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
)

// Server wraps a Collector and serves it over TLS.
type Server struct {
	addr     string
	c        *collect.Collector
	token    string
	certFile string
	keyFile  string
}

// New builds a Server bound to addr (e.g. ":8765"), loading the TLS cert and
// bearer token from the identity dir. It returns an error if the token can't be
// read — the agent must not serve unauthenticated.
func New(addr string, c *collect.Collector, dir string) (*Server, error) {
	token, err := identity.LoadToken(dir)
	if err != nil {
		return nil, err
	}
	return &Server{
		addr:     addr,
		c:        c,
		token:    token,
		certFile: identity.CertPath(dir),
		keyFile:  identity.KeyPath(dir),
	}, nil
}

// Run starts the HTTPS server and blocks.
func (s *Server) Run() error {
	mux := http.NewServeMux()

	// /healthz is unauthenticated: liveness only, no sensitive data.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok", "agent": collect.Version})
	})

	// /v1/status carries the sensitive snapshot and requires the bearer token.
	mux.Handle("GET /v1/status", s.requireToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, s.c.Snapshot())
	})))

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("meerkat-agent listening (https) on %s (GET /v1/status, /healthz)", s.addr)
	return srv.ListenAndServeTLS(s.certFile, s.keyFile)
}

// requireToken rejects any request whose Authorization header doesn't carry the
// agent's bearer token. Comparison is constant-time.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte(s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(bearer(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="meerkat-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer extracts the token from an "Authorization: Bearer <token>" header.
func bearer(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && header[:len(prefix)] == prefix {
		return header[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
