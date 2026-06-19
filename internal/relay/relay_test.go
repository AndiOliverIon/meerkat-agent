package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
)

func TestSettingsFromMapOverridesAgentValues(t *testing.T) {
	settings := SettingsFromMap(map[string]string{
		SnapshotIntervalKey:     "15",
		SettingsRefreshEveryKey: "3",
		"unrelated":             "kept",
	}, DefaultSettings())

	if settings.SnapshotPushInterval != 15*time.Second {
		t.Fatalf("interval = %s, want 15s", settings.SnapshotPushInterval)
	}
	if settings.SettingsRefreshEvery != 3 {
		t.Fatalf("refreshEvery = %d, want 3", settings.SettingsRefreshEvery)
	}
	if settings.Raw["unrelated"] != "kept" {
		t.Fatalf("raw settings = %+v", settings.Raw)
	}
}

func TestRunnerFetchesSettingsAndPostsSnapshot(t *testing.T) {
	var settingsRequested, snapshotPosted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/settings":
			settingsRequested.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer relay-token" {
				t.Fatalf("settings authorization header = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"settings": map[string]string{
					SnapshotIntervalKey:     "1",
					SettingsRefreshEveryKey: "5",
				},
			})
		case "/v1/servers/server-1/snapshot":
			snapshotPosted.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer relay-token" {
				t.Fatalf("authorization header = %q", got)
			}
			if got := r.Header.Get("X-Meerkat-User-ID"); got != "" {
				t.Fatalf("legacy user header = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := Runner{
		BackendURL:          server.URL,
		ServerID:            "server-1",
		UserProfileID:       "profile-1",
		RelayToken:          "relay-token",
		RelayTokenExpiresAt: ptrTime(time.Now().Add(time.Hour)),
		Collector:           collect.New(t.TempDir()),
		Client:              server.Client(),
	}

	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for !settingsRequested.Load() || !snapshotPosted.Load() {
		select {
		case err := <-done:
			t.Fatalf("runner exited early: %v", err)
		case <-deadline:
			t.Fatalf("settingsRequested=%v snapshotPosted=%v", settingsRequested.Load(), snapshotPosted.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerStopsWhenRelayTokenExpired(t *testing.T) {
	var requested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	runner := Runner{
		BackendURL:          server.URL,
		ServerID:            "server-1",
		RelayToken:          "relay-token",
		RelayTokenExpiresAt: ptrTime(time.Unix(1_700_000_000, 0)),
		Collector:           collect.New(t.TempDir()),
		Client:              server.Client(),
		Now:                 func() time.Time { return time.Unix(1_700_000_001, 0) },
	}

	err := runner.Run(context.Background())

	if err == nil {
		t.Fatal("Run succeeded with expired relay token")
	}
	if !strings.Contains(err.Error(), "re-enroll") {
		t.Fatalf("error = %q, want re-enroll guidance", err.Error())
	}
	if requested.Load() {
		t.Fatal("runner made HTTP request with expired relay token")
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
