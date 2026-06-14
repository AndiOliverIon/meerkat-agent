// Package identity manages the agent's per-install secrets: its self-signed TLS
// certificate (+ key) and its bearer auth token. These are GENERATED ONCE at
// install time (the package hook calls GenerateCert/GenerateToken) and only
// READ by the long-running daemon, so the daemon itself writes nothing.
//
// Everything here uses the Go standard library only (no external deps), so the
// cert-generation code is fully auditable in-repo.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// DefaultDir is where the cert, key, and token live as program-generated state.
const DefaultDir = "/var/lib/meerkat-agent"

const (
	certFile  = "cert.pem"
	keyFile   = "key.pem"
	tokenFile = "token"
)

// CertPath, KeyPath, TokenPath return the on-disk locations within dir.
func CertPath(dir string) string  { return filepath.Join(dir, certFile) }
func KeyPath(dir string) string   { return filepath.Join(dir, keyFile) }
func TokenPath(dir string) string { return filepath.Join(dir, tokenFile) }

// GenerateCert creates a self-signed TLS certificate + key in dir if they do
// not already exist (idempotent, so re-running the installer never breaks a
// pairing). Returns whether it created new files.
func GenerateCert(dir string) (created bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	if fileExists(CertPath(dir)) && fileExists(KeyPath(dir)) {
		return false, nil
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return false, err
	}

	host, _ := os.Hostname()
	if host == "" {
		host = "meerkat-agent"
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0), // ~10y; self-signed + pinned, no CA renewal
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{host},
		IPAddresses:           certIPs(),
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return false, err
	}

	// Write cert (0644) and key (0600). Write key first under a temp name and
	// rename so it never exists world-readable mid-write.
	if err := writeFile(CertPath(dir), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return false, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return false, err
	}
	if err := writeFile(KeyPath(dir), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// GenerateToken creates a high-entropy bearer token in dir if one does not
// already exist (idempotent). Returns the token (existing or new).
func GenerateToken(dir string) (token string, created bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	if existing, err := LoadToken(dir); err == nil && existing != "" {
		return existing, false, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", false, err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	if err := writeFile(TokenPath(dir), []byte(tok+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return tok, true, nil
}

// RotateToken replaces the bearer token while leaving the TLS certificate
// unchanged. This supports app re-enrollment and periodic token rotation without
// forcing users to purge/reinstall the agent.
func RotateToken(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	if err := writeFilePreserveOwner(TokenPath(dir), []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// LoadToken reads the bearer token from dir.
func LoadToken(dir string) (string, error) {
	b, err := os.ReadFile(TokenPath(dir))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", errors.New("empty token file")
	}
	return tok, nil
}

// Fingerprint returns the SHA-256 fingerprint of the cert in dir, formatted as
// colon-separated uppercase hex (the value a user verifies at enrollment).
func Fingerprint(dir string) (string, error) {
	b, err := os.ReadFile(CertPath(dir))
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return "", errors.New("cert.pem is not valid PEM")
	}
	sum := sha256.Sum256(block.Bytes)
	parts := make([]string, len(sum))
	for i, by := range sum {
		parts[i] = fmt.Sprintf("%02X", by)
	}
	return strings.Join(parts, ":"), nil
}

// PrimaryIP returns a best-effort non-loopback IPv4 address for display in the
// enrollment string, or a placeholder if none can be determined.
func PrimaryIP() string {
	for _, ip := range hostIPs() {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	return "<server-ip>"
}

// --- helpers ---

func certIPs() []net.IP {
	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ips = append(ips, hostIPs()...)
	return ips
}

func hostIPs() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		out = append(out, ipnet.IP)
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeFilePreserveOwner(path string, data []byte, perm os.FileMode) error {
	var uid, gid int
	preserveOwner := false
	if info, err := os.Stat(path); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			uid, gid = int(st.Uid), int(st.Gid)
			preserveOwner = true
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if preserveOwner {
		if err := os.Chown(tmp, uid, gid); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
