package config

import (
	"errors"
	"os"
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

func TestRelayConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := RelayConfig{
		BackendURL:    " http://127.0.0.1:5281 ",
		ServerID:      " server-1 ",
		UserProfileID: " profile-1 ",
	}
	if err := SaveRelayConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}

	got, err := LoadRelayConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackendURL != "http://127.0.0.1:5281" || got.ServerID != "server-1" || got.UserProfileID != "profile-1" {
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
