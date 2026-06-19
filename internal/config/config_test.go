package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRemoveMSSQLInventory(t *testing.T) {
	dir := t.TempDir()
	if err := SaveMSSQLInventory(dir, MSSQLInventory{Container: "sql-a", Username: "reader", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveMSSQLInventory(dir, MSSQLInventory{Container: "sql-b", Username: "reader", Password: "secret"}); err != nil {
		t.Fatal(err)
	}

	if err := RemoveMSSQLInventory(dir, "sql-a"); err != nil {
		t.Fatal(err)
	}
	configs, err := LoadMSSQLInventories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Container != "sql-b" {
		t.Fatalf("configs after remove = %#v, want only sql-b", configs)
	}

	if err := RemoveMSSQLInventory(dir, "sql-a"); !errors.Is(err, ErrMSSQLInventoryNotFound) {
		t.Fatalf("second remove err = %v, want ErrMSSQLInventoryNotFound", err)
	}
	if err := RemoveMSSQLInventory(dir, "sql-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(MSSQLInventoryPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config file stat err = %v, want not exist", err)
	}
}

func TestMSSQLInventoryPasswordsAreEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	if err := SaveMSSQLInventory(dir, MSSQLInventory{Container: "sql-a", Username: "reader", Password: "secret-password"}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(MSSQLInventoryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-password") {
		t.Fatalf("inventory file contains plaintext password: %s", string(data))
	}
	if !strings.Contains(string(data), "passwordEncrypted") {
		t.Fatalf("inventory file does not contain encrypted password: %s", string(data))
	}
	if _, err := os.Stat(MSSQLInventoryKeyPath(dir)); err != nil {
		t.Fatalf("key file stat err = %v", err)
	}

	configs, err := LoadMSSQLInventories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Password != "secret-password" {
		t.Fatalf("configs = %#v, want decrypted password", configs)
	}
}

func TestMSSQLInventoryLoadsLegacyPlaintextPassword(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(MSSQLInventoryPath(dir), []byte(`[
  {
    "container": "sql-a",
    "username": "reader",
    "password": "legacy-password",
    "updatedAt": "2026-06-19T10:00:00Z"
  }
]`), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadMSSQLInventories(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Password != "legacy-password" {
		t.Fatalf("configs = %#v, want legacy plaintext password", configs)
	}
}

func TestRelayConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := RelayConfig{
		BackendURL:    " http://127.0.0.1:5281 ",
		ServerID:      " server-1 ",
		UserProfileID: " profile-1 ",
		RelayToken:    " relay-token ",
	}
	if err := SaveRelayConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := LoadRelayConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackendURL != "http://127.0.0.1:5281" || got.ServerID != "server-1" || got.UserProfileID != "profile-1" || got.RelayToken != "relay-token" {
		t.Fatalf("relay config = %#v", got)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt was not set")
	}

	if err := RemoveRelayConfig(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRelayConfig(dir); !errors.Is(err, ErrRelayConfigNotFound) {
		t.Fatalf("load after remove err = %v, want ErrRelayConfigNotFound", err)
	}
}
