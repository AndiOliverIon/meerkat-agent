package identity

import (
	"crypto/tls"
	"os"
	"strings"
	"testing"
)

func TestGenerateCertIsUsableAndIdempotent(t *testing.T) {
	dir := t.TempDir()

	created, err := GenerateCert(dir)
	if err != nil {
		t.Fatalf("GenerateCert: %v", err)
	}
	if !created {
		t.Fatal("expected created=true on first generation")
	}

	// The cert+key must load as a usable TLS keypair.
	if _, err := tls.LoadX509KeyPair(CertPath(dir), KeyPath(dir)); err != nil {
		t.Fatalf("generated cert/key is not a valid TLS pair: %v", err)
	}

	// Private key must not be world-readable.
	fi, err := os.Stat(KeyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("key perms too open: %v", fi.Mode().Perm())
	}

	// Second call must be a no-op (idempotent) so pairing survives reinstalls.
	created2, err := GenerateCert(dir)
	if err != nil {
		t.Fatalf("GenerateCert (2nd): %v", err)
	}
	if created2 {
		t.Error("expected created=false on second generation")
	}

	fp, err := Fingerprint(dir)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !strings.Contains(fp, ":") || len(fp) < 32 {
		t.Errorf("fingerprint looks wrong: %q", fp)
	}
}

func TestGenerateTokenIdempotentAndLoadable(t *testing.T) {
	dir := t.TempDir()

	tok, created, err := GenerateToken(dir)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !created || tok == "" {
		t.Fatalf("expected a new non-empty token, got created=%v tok=%q", created, tok)
	}

	loaded, err := LoadToken(dir)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if loaded != tok {
		t.Errorf("LoadToken = %q, want %q", loaded, tok)
	}

	tok2, created2, err := GenerateToken(dir)
	if err != nil {
		t.Fatalf("GenerateToken (2nd): %v", err)
	}
	if created2 {
		t.Error("expected created=false on second token generation")
	}
	if tok2 != tok {
		t.Error("second GenerateToken changed the token")
	}
}
