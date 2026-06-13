package collect

import (
	"testing"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

func TestGroupKey(t *testing.T) {
	cases := map[string]string{
		"bookinglounge-web":      "bookinglounge",
		"bookinglounge_db_1":     "bookinglounge",
		"api.bookinglounge.ro":   "bookinglounge",
		"www.bookinglounge.ro":   "bookinglounge",
		"/tally-postgres":        "tally",
		"https://staging.app.io": "staging", // tld stripped; all tokens generic, falls back to first
		"redis":                  "redis",
		"alice.tnisoft.ro":       "alice",
	}
	for in, want := range cases {
		if got := groupKey(in); got != want {
			t.Errorf("groupKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildGroupsClustersByRoot(t *testing.T) {
	containers := []model.Container{
		{Name: "bookinglounge-web", Image: "nginx", Running: true},
		{Name: "tally-app", Image: "tally:latest", Running: false},
	}
	databases := []model.Database{
		{Name: "bookinglounge", Engine: "MySQL", SizeGB: f64ptr(2.5), Status: "ok"},
	}
	endpoints := []model.Endpoint{
		{Name: "api.bookinglounge.ro", URL: "https://api.bookinglounge.ro/", Reachable: true, StatusCode: intptr(200)},
		{Name: "tally.tnisoft.ro", URL: "https://tally.tnisoft.ro/", Reachable: false},
	}

	groups := buildGroups(containers, databases, endpoints)

	byName := map[string]model.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}

	bl, ok := byName["bookinglounge"]
	if !ok {
		t.Fatalf("expected a 'bookinglounge' group, got %v", groups)
	}
	if len(bl.Components) != 3 {
		t.Errorf("bookinglounge: expected 3 components (container+db+endpoint), got %d", len(bl.Components))
	}

	tally, ok := byName["tally"]
	if !ok {
		t.Fatalf("expected a 'tally' group, got %v", groups)
	}
	if len(tally.Components) != 2 {
		t.Errorf("tally: expected 2 components, got %d", len(tally.Components))
	}
}

func TestBuildGroupsEmpty(t *testing.T) {
	groups := buildGroups(nil, nil, nil)
	if groups == nil {
		t.Fatal("buildGroups should return a non-nil empty slice for JSON stability")
	}
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}
}
