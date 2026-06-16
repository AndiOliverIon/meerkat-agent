package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
)

func TestRequireToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(identity.TokenPath(dir), []byte("s3cr3t-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{identityDir: dir}
	handler := s.requireToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"not bearer", "s3cr3t-token", http.StatusUnauthorized},
		{"correct token", "Bearer s3cr3t-token", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
			if c.header != "" {
				req.Header.Set("Authorization", c.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

func TestRequireTokenReloadsRotatedToken(t *testing.T) {
	oldTTL := tokenCacheTTL
	tokenCacheTTL = 0
	defer func() { tokenCacheTTL = oldTTL }()

	dir := t.TempDir()
	if err := os.WriteFile(identity.TokenPath(dir), []byte("old-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{identityDir: dir}
	handler := s.requireToken(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer old-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("old token before rotation status = %d, want %d", rec.Code, http.StatusOK)
	}

	if err := os.WriteFile(identity.TokenPath(dir), []byte("new-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer old-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer new-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new token after rotation status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestRequireTokenCachesBriefly(t *testing.T) {
	oldTTL := tokenCacheTTL
	tokenCacheTTL = time.Minute
	defer func() { tokenCacheTTL = oldTTL }()

	dir := t.TempDir()
	if err := os.WriteFile(identity.TokenPath(dir), []byte("old-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{identityDir: dir}
	if got, err := s.loadToken(); err != nil || got != "old-token" {
		t.Fatalf("first loadToken = %q, %v; want old-token, nil", got, err)
	}
	if err := os.WriteFile(identity.TokenPath(dir), []byte("new-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.loadToken(); err != nil || got != "old-token" {
		t.Fatalf("cached loadToken = %q, %v; want old-token, nil", got, err)
	}
}

func TestBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer abc": "abc",
		"Bearer ":    "",
		"abc":        "",
		"":           "",
		"bearer abc": "abc",
		"Bearer a b": "a b",
	}
	for in, want := range cases {
		if got := bearer(in); got != want {
			t.Errorf("bearer(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleDeleteMSSQLConfig(t *testing.T) {
	dir := t.TempDir()
	if err := agentconfig.SaveMSSQLInventory(dir, agentconfig.MSSQLInventory{
		Container: "sql-a",
		Username:  "reader",
		Password:  "secret",
	}); err != nil {
		t.Fatal(err)
	}

	s := &Server{identityDir: dir}
	req := httptest.NewRequest(http.MethodDelete, "/v1/config/mssql/sql-a", nil)
	req.SetPathValue("container", "sql-a")
	rec := httptest.NewRecorder()
	s.handleDeleteMSSQLConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body %q; want 200", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/config/mssql/sql-a", nil)
	req.SetPathValue("container", "sql-a")
	rec = httptest.NewRecorder()
	s.handleDeleteMSSQLConfig(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
}
