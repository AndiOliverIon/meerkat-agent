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
