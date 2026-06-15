//go:build linux

package collect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMSSQLDatabaseNameFromFile(t *testing.T) {
	tests := map[string]string{
		"ArdisDemo228.mdf":     "ArdisDemo228",
		"ArdisDemo228_log.ldf": "ArdisDemo228",
		"ArdisDemo228_2.ndf":   "ArdisDemo228_2",
		"templog.ldf":          "tempdb",
		"mastlog.ldf":          "master",
		"msdbdata.mdf":         "msdb",
	}

	for input, want := range tests {
		if got := mssqlDatabaseNameFromFile(input); got != want {
			t.Fatalf("mssqlDatabaseNameFromFile(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParsePostgreSQLInventory(t *testing.T) {
	dbs := parsePostgreSQLInventory([]byte("appdb|1234567890\npostgres|8192\nignored\n"))

	if len(dbs) != 2 {
		t.Fatalf("len(parsePostgreSQLInventory) = %d, want 2", len(dbs))
	}
	if dbs[0].Name != "appdb" || dbs[0].Engine != "PostgreSQL" || dbs[0].Status != "running" {
		t.Fatalf("first db = %#v", dbs[0])
	}
	if dbs[0].SizeGB == nil || *dbs[0].SizeGB != 1.2 {
		t.Fatalf("appdb size = %v, want 1.2", dbs[0].SizeGB)
	}
	if dbs[1].Name != "postgres" {
		t.Fatalf("second db name = %q, want postgres", dbs[1].Name)
	}
}

func TestPostgresDataDirs(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "postgresql", "16", "main")
	dataDir := filepath.Join(root, "pgsql", "data")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainDir, "PG_VERSION"), []byte("16\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "PG_VERSION"), []byte("15\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := postgresDataDirs([]string{filepath.Join(root, "postgresql"), filepath.Join(root, "pgsql")})
	want := []string{dataDir, mainDir}
	if len(got) != len(want) {
		t.Fatalf("postgresDataDirs len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("postgresDataDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPostgresClusterName(t *testing.T) {
	if got, want := postgresClusterName("/var/lib/postgresql/16/main"), "PostgreSQL 16/main cluster"; got != want {
		t.Fatalf("postgresClusterName debian = %q, want %q", got, want)
	}
	if got, want := postgresClusterName("/var/lib/pgsql/data"), "PostgreSQL data cluster"; got != want {
		t.Fatalf("postgresClusterName pgsql = %q, want %q", got, want)
	}
}
