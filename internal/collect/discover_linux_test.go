//go:build linux

package collect

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
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

func TestScanMSSQLDataDirSkipsSystemDatabases(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"ArdisDemo228.mdf",
		"ArdisDemo228_log.ldf",
		"master.mdf",
		"mastlog.ldf",
		"model.mdf",
		"modellog.ldf",
		"msdbdata.mdf",
		"msdblog.ldf",
		"tempdb.mdf",
		"templog.ldf",
		"model_msdbdata.mdf",
		"model_msdblog.ldf",
		"model_replicatedmaster.mdf",
		"model_replicatedmaster_log.ldf",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := scanMSSQLDataDir(dir)
	if len(got) != 1 {
		t.Fatalf("scanMSSQLDataDir len = %d, want 1: %#v", len(got), got)
	}
	if got[0].Name != "ArdisDemo228" {
		t.Fatalf("scanMSSQLDataDir database = %q, want ArdisDemo228", got[0].Name)
	}
}

func TestParseMSSQLInventorySkipsSystemDatabases(t *testing.T) {
	got := parseMSSQLInventory([]byte("master|10\napp|42.5\nmodel|1\nmsdb|2\ntempdb|3\narchive|7\n"))

	if len(got) != 2 {
		t.Fatalf("parseMSSQLInventory len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "app" || got[1].Name != "archive" {
		t.Fatalf("parseMSSQLInventory = %#v, want app and archive only", got)
	}
}

func TestParseMSSQLInventoryReadsSQLMetadata(t *testing.T) {
	got := parseMSSQLInventory([]byte("app|42.5|ONLINE|FULL|2026-06-09T10:01:02.123\n"))

	if len(got) != 1 {
		t.Fatalf("parseMSSQLInventory len = %d, want 1: %#v", len(got), got)
	}
	db := got[0]
	if db.Name != "app" || db.Status != "running" {
		t.Fatalf("db = %#v, want running app", db)
	}
	if db.State == nil || *db.State != "ONLINE" {
		t.Fatalf("state = %v, want ONLINE", db.State)
	}
	if db.RecoveryModel == nil || *db.RecoveryModel != "FULL" {
		t.Fatalf("recovery model = %v, want FULL", db.RecoveryModel)
	}
	if db.CreatedAt == nil || db.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") != "2026-06-09T10:01:02.123Z" {
		t.Fatalf("createdAt = %v, want parsed timestamp", db.CreatedAt)
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
	if got, want := postgresClusterName("/var/lib/postgresql/main"), "PostgreSQL main cluster"; got != want {
		t.Fatalf("postgresClusterName no version = %q, want %q", got, want)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"readonly":         "'readonly'",
		"space value":      "'space value'",
		"quoted'value":     "'quoted'\"'\"'value'",
		"$(touch /tmp/no)": "'$(touch /tmp/no)'",
	}
	for input, want := range tests {
		if got := shellQuote(input); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDockerDemux(t *testing.T) {
	raw := appendDockerFrame(nil, 1, []byte("hello\n"))
	raw = appendDockerFrame(raw, 2, []byte("warn\n"))
	if got, want := string(dockerDemux(raw)), "hello\nwarn\n"; got != want {
		t.Fatalf("dockerDemux framed = %q, want %q", got, want)
	}

	plain := []byte("plain output\n")
	if got := string(dockerDemux(plain)); got != string(plain) {
		t.Fatalf("dockerDemux plain = %q, want %q", got, plain)
	}

	truncated := appendDockerFrame(nil, 1, []byte("complete"))
	truncated = append(truncated, []byte{1, 0, 0, 0, 0, 0, 0, 20, 'x'}...)
	if got, want := string(dockerDemux(truncated)), "complete"; got != want {
		t.Fatalf("dockerDemux truncated = %q, want %q", got, want)
	}
}

func appendDockerFrame(out []byte, stream byte, payload []byte) []byte {
	header := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[4:8], uint32(len(payload)))
	out = append(out, header...)
	return append(out, payload...)
}

func TestMergeMSSQLDatabaseInventory(t *testing.T) {
	runningSize := 1.5
	fileSize := 2.0
	primary := []model.Database{{Name: "app", Engine: "MSSQL", SizeGB: &runningSize, Status: "running"}}
	fallback := []model.Database{
		{Name: "APP", Engine: "MSSQL", SizeGB: &fileSize, Status: "detected"},
		{Name: "archive", Engine: "MSSQL", Status: "detected"},
	}

	got := mergeMSSQLDatabaseInventory(primary, fallback)
	if len(got) != 2 {
		t.Fatalf("mergeMSSQLDatabaseInventory len = %d, want 2: %#v", len(got), got)
	}
	if got[0].Name != "app" || got[0].Status != "running" {
		t.Fatalf("first merged db = %#v, want running app from primary", got[0])
	}
	if got[1].Name != "archive" {
		t.Fatalf("second merged db = %#v, want archive fallback", got[1])
	}
}

func TestIsMSSQLContainer(t *testing.T) {
	tests := []struct {
		name string
		c    model.Container
		want bool
	}{
		{"official image", model.Container{Name: "db", Image: "mcr.microsoft.com/mssql/server:2022-latest"}, true},
		{"azure edge image", model.Container{Name: "db", Image: "mcr.microsoft.com/azure-sql-edge:latest"}, true},
		{"simple name", model.Container{Name: "mssql", Image: "ubuntu"}, true},
		{"dismiss false positive", model.Container{Name: "dismiss-sql-server-cache", Image: "redis:7"}, false},
		{"postgres", model.Container{Name: "postgres", Image: "postgres:16"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMSSQLContainer(tt.c); got != tt.want {
				t.Fatalf("isMSSQLContainer(%#v) = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}

func TestValidHost(t *testing.T) {
	tests := map[string]bool{
		"example.com":       true,
		"api.example.co.uk": true,
		"localhost":         false,
		"localhost.local":   false,
		"example":           false,
		"*.example.com":     false,
		"_":                 false,
		"bad..example.com":  false,
		"-bad.example.com":  false,
		"bad-.example.com":  false,
		"bad_example.com":   false,
	}
	for input, want := range tests {
		if got := validHost(input); got != want {
			t.Fatalf("validHost(%q) = %v, want %v", input, got, want)
		}
	}
}
