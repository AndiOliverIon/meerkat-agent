package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

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

func TestBearer(t *testing.T) {
	cases := map[string]string{
		"Bearer abc": "abc",
		"Bearer ":    "",
		"abc":        "",
		"":           "",
		"bearer abc": "", // case-sensitive scheme
		"Bearer a b": "a b",
	}
	for in, want := range cases {
		if got := bearer(in); got != want {
			t.Errorf("bearer(%q) = %q, want %q", in, got, want)
		}
	}
}
