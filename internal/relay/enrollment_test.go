package relay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecodeEnrollmentCode(t *testing.T) {
	code := encodeTestEnrollmentCode(t, EnrollmentCode{
		BackendURL: "http://127.0.0.1:5281/",
		Code:       "secret-code",
	})

	decoded, err := DecodeEnrollmentCode(code)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BackendURL != "http://127.0.0.1:5281" || decoded.Code != "secret-code" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestConsumeEnrollmentCodeReturnsRelayConfig(t *testing.T) {
	var sawFingerprint bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/relay-enroll" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var payload struct {
			EnrollmentCode string `json:"enrollmentCode"`
			Fingerprint    string `json:"fingerprint"`
			AgentVersion   string `json:"agentVersion"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		sawFingerprint = payload.Fingerprint == "AA:BB" && payload.AgentVersion == "0.1.0"
		_ = json.NewEncoder(w).Encode(map[string]string{
			"backendUrl":    serverURL(r),
			"serverId":      "server-1",
			"userProfileId": "profile-1",
			"relayToken":    "relay-token",
		})
	}))
	defer server.Close()

	code := encodeTestEnrollmentCode(t, EnrollmentCode{BackendURL: server.URL, Code: "secret-code"})
	cfg, err := ConsumeEnrollmentCode(context.Background(), code, "AA:BB", "0.1.0", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if !sawFingerprint {
		t.Fatal("fingerprint/version were not sent")
	}
	if cfg.BackendURL != server.URL || cfg.ServerID != "server-1" || cfg.UserProfileID != "profile-1" || cfg.RelayToken != "relay-token" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func encodeTestEnrollmentCode(t *testing.T, code EnrollmentCode) string {
	t.Helper()
	data, err := json.Marshal(code)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
