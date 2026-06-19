package main

import (
	"testing"

	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
)

func TestRelayConfigForDisplayRedactsToken(t *testing.T) {
	cfg := relayConfigForDisplay(agentconfig.RelayConfig{
		BackendURL: "https://api.example.com",
		ServerID:   "server-1",
		RelayToken: "secret-relay-token",
	})

	if cfg.RelayToken == "secret-relay-token" {
		t.Fatal("relay token was not redacted")
	}
	if cfg.RelayToken != "redacted" {
		t.Fatalf("RelayToken = %q, want redacted", cfg.RelayToken)
	}
	if cfg.BackendURL != "https://api.example.com" || cfg.ServerID != "server-1" {
		t.Fatalf("non-secret fields changed: %+v", cfg)
	}
}

func TestRelayTokenFromEnvTrimsWhitespace(t *testing.T) {
	t.Setenv("MEERKAT_RELAY_TOKEN", " secret-token \n")

	if got := relayTokenFromEnv(); got != "secret-token" {
		t.Fatalf("relayTokenFromEnv() = %q, want secret-token", got)
	}
}
