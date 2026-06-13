// Group synthesis is portable: it turns the platform-discovered containers,
// databases, and endpoints into "apps" by grouping components that share a
// common name root (e.g. the container "bookinglounge-web", the database
// "bookinglounge", and the endpoint "api.bookinglounge.ro" all collapse to the
// "bookinglounge" group). The heuristic lives in one place so it is easy to
// audit and tune.
package collect

import (
	"sort"
	"strings"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

// buildGroups clusters discovered components by their name root.
func buildGroups(containers []model.Container, databases []model.Database, endpoints []model.Endpoint) []model.Group {
	byKey := map[string][]model.Component{}
	add := func(name string, c model.Component) {
		key := groupKey(name)
		byKey[key] = append(byKey[key], c)
	}

	for _, c := range containers {
		status := "ok"
		if !c.Running {
			status = "offline"
		}
		add(c.Name, model.Component{Name: c.Name, Kind: "container", Status: status, Detail: c.Image})
	}
	for _, d := range databases {
		add(d.Name, model.Component{Name: d.Name, Kind: "database", StorageGB: d.SizeGB, Status: d.Status, Detail: d.Engine})
	}
	for _, e := range endpoints {
		status := "ok"
		if !e.Reachable {
			status = "critical"
		}
		add(e.Name, model.Component{Name: e.Name, Kind: "endpoint", Status: status, Detail: e.URL})
	}

	if len(byKey) == 0 {
		return []model.Group{}
	}

	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	groups := make([]model.Group, 0, len(keys))
	for _, k := range keys {
		comps := byKey[k]
		sort.Slice(comps, func(a, b int) bool { return comps[a].Name < comps[b].Name })
		groups = append(groups, model.Group{Name: k, Components: comps})
	}
	return groups
}

// genericTokens are role/environment words that never identify an app on their
// own, so they are skipped when choosing a group key.
var genericTokens = map[string]bool{
	"www": true, "api": true, "app": true, "web": true, "srv": true, "server": true,
	"svc": true, "service": true, "staging": true, "stage": true, "prod": true,
	"production": true, "dev": true, "test": true, "qa": true, "main": true,
	"primary": true, "replica": true, "db": true, "database": true, "cache": true,
	"redis": true, "pg": true, "postgres": true, "postgresql": true, "mysql": true,
	"mariadb": true, "mongo": true, "mongodb": true, "worker": true, "queue": true,
}

// tldTokens are common public suffixes stripped from hostnames before keying.
var tldTokens = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "co": true, "dev": true,
	"app": true, "cloud": true, "xyz": true, "info": true, "ro": true, "eu": true,
	"uk": true, "us": true, "de": true, "fr": true, "online": true, "site": true,
}

// groupKey reduces a component name to the token most likely to identify the
// application it belongs to.
func groupKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.TrimPrefix(s, "/")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?:"); i >= 0 {
		s = s[:i]
	}

	tokens := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' '
	})

	cleaned := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if t == "" || tldTokens[t] || isAllDigits(t) {
			continue
		}
		cleaned = append(cleaned, t)
	}

	for _, t := range cleaned {
		if !genericTokens[t] {
			return t
		}
	}
	if len(cleaned) > 0 {
		return cleaned[0]
	}
	if s == "" {
		return name
	}
	return s
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
