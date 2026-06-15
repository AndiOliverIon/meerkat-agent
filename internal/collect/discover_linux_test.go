//go:build linux

package collect

import "testing"

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
