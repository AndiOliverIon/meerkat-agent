// Package server exposes the agent snapshot over HTTPS: TLS with the agent's
// self-signed cert, and a per-install bearer token on the sensitive endpoint.
package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
)

// Server wraps a Collector and serves it over TLS.
type Server struct {
	addr        string
	c           *collect.Collector
	identityDir string
	certFile    string
	keyFile     string

	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time
}

var validContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)
var tokenCacheTTL = time.Second

// New builds a Server bound to addr (e.g. ":8765"), loading the TLS cert and
// bearer token from the identity dir. It returns an error if the token can't be
// read — the agent must not serve unauthenticated.
func New(addr string, c *collect.Collector, dir string) (*Server, error) {
	if _, err := identity.LoadToken(dir); err != nil {
		return nil, err
	}
	return &Server{
		addr:        addr,
		c:           c,
		identityDir: dir,
		certFile:    identity.CertPath(dir),
		keyFile:     identity.KeyPath(dir),
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

	mux.Handle("POST /v1/config/mssql", s.requireToken(http.HandlerFunc(s.handleMSSQLConfig)))
	mux.Handle("DELETE /v1/config/mssql/{container}", s.requireToken(http.HandlerFunc(s.handleDeleteMSSQLConfig)))

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("meerkat-agent shutdown: %v", err)
		}
	}()

	log.Printf("meerkat-agent listening (https) on %s (GET /v1/status, /healthz)", s.addr)
	if err := srv.ListenAndServeTLS(s.certFile, s.keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleMSSQLConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Container string `json:"container"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.Container = strings.TrimSpace(req.Container)
	req.Username = strings.TrimSpace(req.Username)
	if err := validateMSSQLConfigRequest(req.Container, req.Username, req.Password); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	databases, err := collect.TestMSSQLInventory(req.Container, req.Username, req.Password)
	if err != nil {
		http.Error(w, "could not verify MSSQL read-only inventory credentials", http.StatusBadRequest)
		return
	}

	cfg := agentconfig.MSSQLInventory{
		Container: req.Container,
		Username:  req.Username,
		Password:  req.Password,
	}
	if err := agentconfig.SaveMSSQLInventory(s.identityDir, cfg); err != nil {
		http.Error(w, "could not store MSSQL inventory config", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"status":    "configured",
		"config":    agentconfig.SummarizeMSSQLInventory(cfg),
		"databases": databases,
	})
}

func (s *Server) handleDeleteMSSQLConfig(w http.ResponseWriter, r *http.Request) {
	container := strings.TrimSpace(r.PathValue("container"))
	if err := validateMSSQLContainer(container); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := agentconfig.RemoveMSSQLInventory(s.identityDir, container); err != nil {
		if errors.Is(err, agentconfig.ErrMSSQLInventoryNotFound) {
			http.Error(w, "MSSQL inventory config not found", http.StatusNotFound)
			return
		}
		http.Error(w, "could not remove MSSQL inventory config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "removed", "container": container})
}

func validateMSSQLConfigRequest(container, username, password string) error {
	if err := validateMSSQLContainer(container); err != nil {
		return err
	}
	if username == "" {
		return errors.New("username is required")
	}
	if password == "" {
		return errors.New("password is required")
	}
	return nil
}

func validateMSSQLContainer(container string) error {
	if container == "" {
		return errors.New("container is required")
	}
	if !validContainerName.MatchString(container) {
		return errors.New("container name contains invalid characters")
	}
	return nil
}

// requireToken rejects any request whose Authorization header doesn't carry the
// agent's bearer token. Comparison is constant-time.
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := s.loadToken()
		if err != nil {
			http.Error(w, "agent token unavailable", http.StatusServiceUnavailable)
			return
		}
		want := []byte(token)
		got := []byte(bearer(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="meerkat-agent"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) loadToken() (string, error) {
	now := time.Now()
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if s.token != "" && now.Before(s.tokenUntil) {
		return s.token, nil
	}
	token, err := identity.LoadToken(s.identityDir)
	if err != nil {
		s.token = ""
		s.tokenUntil = time.Time{}
		return "", err
	}
	s.token = token
	s.tokenUntil = now.Add(tokenCacheTTL)
	return token, nil
}

// bearer extracts the token from an "Authorization: Bearer <token>" header.
func bearer(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return header[len(prefix):]
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data = append(data, '\n')
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
