//go:build linux

// Linux resource discovery for the snapshot's groups/containers/databases/
// endpoints fields. Everything here is read-only and self-contained: the agent
// discovers what's running by reading what is *already on the box* (the Docker
// socket, /proc, well-known data directories, and the local web-server config)
// and never takes configuration, credentials, or external input.
package collect

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

// ---------------------------------------------------------------------------
// Containers — read from the Docker Engine API over its unix socket.
// ---------------------------------------------------------------------------

const dockerSocket = "/var/run/docker.sock"

// dockerHTTP returns an http.Client that speaks to the Docker socket. The host
// part of the URL is irrelevant; the dialer always connects to the socket.
func dockerHTTP() *http.Client {
	return &http.Client{
		Timeout: 4 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", dockerSocket)
			},
		},
	}
}

// readContainers lists Docker containers via the read-only Docker API.
// Returns nil ("not obtained" -> null) if Docker isn't installed or the socket
// isn't reachable; returns a (possibly empty) slice when Docker answered.
func readContainers() []model.Container {
	if _, err := os.Stat(dockerSocket); err != nil {
		return nil
	}
	client := dockerHTTP()

	resp, err := client.Get("http://docker/v1.41/containers/json?all=1")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var raw []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}

	out := make([]model.Container, len(raw))
	var wg sync.WaitGroup
	for i, c := range raw {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		running := c.State == "running"
		out[i] = model.Container{Name: name, Image: c.Image, Running: running}
		if running {
			wg.Add(1)
			go func(idx int, id string) {
				defer wg.Done()
				// CPU/MemMB stay nil ("N/A") unless stats were obtained.
				if cpu, mem, ok := containerStats(client, id); ok {
					out[idx].CPU = intptr(cpu)
					out[idx].MemMB = intptr(mem)
				}
			}(i, c.ID)
		}
	}
	wg.Wait()

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// containerStats reads two frames (~1s apart) from the Docker stats stream and
// derives CPU% and memory in MB the same way `docker stats` does. ok is false
// if no stats frame could be read.
func containerStats(client *http.Client, id string) (cpuPercent, memMB int, ok bool) {
	resp, err := client.Get("http://docker/v1.41/containers/" + id + "/stats?stream=true")
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()

	type cpuUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	}
	type cpuStats struct {
		CPUUsage    cpuUsage `json:"cpu_usage"`
		SystemUsage uint64   `json:"system_cpu_usage"`
		OnlineCPUs  int      `json:"online_cpus"`
	}
	type memStats struct {
		Usage uint64            `json:"usage"`
		Stats map[string]uint64 `json:"stats"`
	}
	var frame struct {
		CPU    cpuStats `json:"cpu_stats"`
		PreCPU cpuStats `json:"precpu_stats"`
		Memory memStats `json:"memory_stats"`
	}

	dec := json.NewDecoder(resp.Body)
	got := 0
	for n := 0; n < 2; n++ {
		if err := dec.Decode(&frame); err != nil {
			break
		}
		got++
	}
	if got == 0 {
		return 0, 0, false
	}

	cpuDelta := float64(frame.CPU.CPUUsage.TotalUsage) - float64(frame.PreCPU.CPUUsage.TotalUsage)
	sysDelta := float64(frame.CPU.SystemUsage) - float64(frame.PreCPU.SystemUsage)
	cpus := frame.CPU.OnlineCPUs
	if cpus == 0 {
		cpus = 1
	}
	if sysDelta > 0 && cpuDelta > 0 {
		cpuPercent = int(cpuDelta / sysDelta * float64(cpus) * 100)
	}

	// Subtract page cache so the figure reflects working-set memory, matching
	// `docker stats`. Key name differs between cgroup v1 ("cache") and v2
	// ("inactive_file").
	usage := frame.Memory.Usage
	if cache, ok := frame.Memory.Stats["inactive_file"]; ok && cache <= usage {
		usage -= cache
	} else if cache, ok := frame.Memory.Stats["cache"]; ok && cache <= usage {
		usage -= cache
	}
	memMB = int(usage / (1024 * 1024))
	return cpuPercent, memMB, true
}

// ---------------------------------------------------------------------------
// Databases — detect engines from the process table, size from data dirs.
// No connection and no credentials: only the filesystem and /proc are read.
// ---------------------------------------------------------------------------

type dbEngine struct {
	engine   string
	procs    []string // process comm names that indicate this engine
	dataDirs []string // candidate data directories
	perDB    bool     // subdirectories of the data dir map to database names
}

var knownDBs = []dbEngine{
	{engine: "PostgreSQL", procs: []string{"postgres"}, dataDirs: []string{"/var/lib/postgresql", "/var/lib/pgsql"}},
	{engine: "MySQL", procs: []string{"mysqld", "mariadbd"}, dataDirs: []string{"/var/lib/mysql"}, perDB: true},
	{engine: "Redis", procs: []string{"redis-server"}, dataDirs: []string{"/var/lib/redis"}},
	{engine: "MongoDB", procs: []string{"mongod"}, dataDirs: []string{"/var/lib/mongodb", "/var/lib/mongo"}},
}

// readDatabases reports each detected database engine, its on-disk size, and
// whether it is currently running. For MySQL/MariaDB each schema directory is
// reported individually so the app shows a real per-database list.
func readDatabases() []model.Database {
	procs := runningProcs()
	var out []model.Database

	for _, db := range knownDBs {
		running := false
		for _, p := range db.procs {
			if procs[p] {
				running = true
				break
			}
		}
		dataDir := firstExistingDir(db.dataDirs)
		if !running && dataDir == "" {
			continue // engine neither running nor installed
		}

		status := "offline"
		if running {
			status = "ok"
		}

		if db.perDB && dataDir != "" {
			if schemas := mysqlSchemas(dataDir); len(schemas) > 0 {
				for _, s := range schemas {
					out = append(out, model.Database{
						Name:   s.name,
						Engine: db.engine,
						SizeGB: s.sizeGB, // nil if the schema dir wasn't readable
						Status: status,
					})
				}
				continue
			}
		}

		// Cluster-level entry. SizeGB is nil ("N/A") when there's no data dir or
		// it couldn't be read (e.g. permissions).
		var size *float64
		if dataDir != "" {
			size = dirSizeGB(dataDir)
		}
		out = append(out, model.Database{
			Name:   db.engine,
			Engine: db.engine,
			SizeGB: size,
			Status: status,
		})
	}

	if out == nil {
		return []model.Database{}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// runningProcs returns the set of process "comm" names currently running.
func runningProcs() map[string]bool {
	set := map[string]bool{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return set
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue // not a pid directory
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue
		}
		set[strings.TrimSpace(string(b))] = true
	}
	return set
}

func firstExistingDir(paths []string) string {
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

type schemaDir struct {
	name   string
	sizeGB *float64 // nil if the schema dir couldn't be read
}

// mysqlSchemas treats each non-system subdirectory of the MySQL data dir as a
// database schema and sizes it on disk.
func mysqlSchemas(dataDir string) []schemaDir {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	system := map[string]bool{"mysql": true, "performance_schema": true, "sys": true}
	var out []schemaDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if system[name] || strings.HasPrefix(name, "#") {
			continue
		}
		out = append(out, schemaDir{name: name, sizeGB: dirSizeGB(filepath.Join(dataDir, name))})
	}
	return out
}

// dirSizeGB sums the apparent size of all files under root and returns it
// rounded, or nil ("N/A") if root itself can't be accessed (e.g. permission
// denied). Individual unreadable entries deeper in the tree are skipped.
func dirSizeGB(root string) *float64 {
	if _, err := os.Stat(root); err != nil {
		return nil // can't even reach the data dir -> not obtained
	}
	var total int64
	accessible := false
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entry, keep walking
		}
		accessible = true
		if d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	if !accessible {
		return nil // couldn't read anything under root
	}
	return f64ptr(round1(float64(total) / 1e9))
}

// ---------------------------------------------------------------------------
// Endpoints — discover the hostnames this box serves from its own web-server
// configuration, then probe each for reachability. The agent only ever
// contacts hostnames already configured on this machine.
// ---------------------------------------------------------------------------

const maxEndpoints = 25

func readEndpoints() []model.Endpoint {
	hosts := discoverHostnames()
	if len(hosts) == 0 {
		return []model.Endpoint{}
	}

	out := make([]model.Endpoint, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		wg.Add(1)
		go func(idx int, host string) {
			defer wg.Done()
			out[idx] = probeEndpoint(host)
		}(i, h)
	}
	wg.Wait()

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// discoverHostnames reads nginx, Apache, and Caddy config to collect the
// hostnames this server is configured to serve.
func discoverHostnames() []string {
	set := map[string]bool{}

	for _, dir := range []string{"/etc/nginx/sites-enabled", "/etc/nginx/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "server_name", set)
		}
	}
	for _, dir := range []string{"/etc/apache2/sites-enabled", "/etc/httpd/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "servername", set)
			scanDirective(f, "serveralias", set)
		}
	}
	scanCaddyfile("/etc/caddy/Caddyfile", set)

	var out []string
	for h := range set {
		if validHost(h) {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	if len(out) > maxEndpoints {
		out = out[:maxEndpoints]
	}
	return out
}

func listFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	return out
}

// scanDirective collects the arguments of a config directive (e.g. nginx
// "server_name a.com b.com;" or Apache "ServerName a.com") from a file.
func scanDirective(path, directive string, set map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.ToLower(fields[0]) != directive {
			continue
		}
		for _, tok := range fields[1:] {
			tok = strings.Trim(strings.TrimRight(tok, ";"), `"'`)
			if tok == "" || tok == "_" {
				continue
			}
			set[strings.ToLower(tok)] = true
		}
	}
}

// scanCaddyfile collects site addresses from a Caddyfile (lines that open a
// site block, e.g. "example.com, www.example.com {").
func scanCaddyfile(path string, set map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "{") {
			continue
		}
		addr := strings.TrimSpace(line[:strings.Index(line, "{")])
		for _, tok := range strings.Split(addr, ",") {
			tok = strings.TrimSpace(tok)
			tok = strings.TrimPrefix(tok, "https://")
			tok = strings.TrimPrefix(tok, "http://")
			if i := strings.IndexAny(tok, "/:"); i >= 0 {
				tok = tok[:i]
			}
			if tok == "" || tok == "_" {
				continue
			}
			set[strings.ToLower(tok)] = true
		}
	}
}

// validHost rejects wildcards, catch-alls, and anything that isn't a plausible
// DNS hostname so the agent never probes a junk target.
func validHost(h string) bool {
	if h == "" || h == "_" || strings.HasPrefix(h, "*") || strings.HasPrefix(h, "localhost") {
		return false
	}
	if !strings.Contains(h, ".") {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

// probeClient records the first response instead of following redirects, so a
// http->https redirect is reported as reachable rather than chased into a loop.
var probeClient = &http.Client{
	Timeout: 3 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func probeEndpoint(host string) model.Endpoint {
	for _, scheme := range []string{"https", "http"} {
		url := scheme + "://" + host + "/"
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "meerkat-agent/"+Version)

		start := time.Now()
		resp, err := probeClient.Do(req)
		ms := int(time.Since(start).Milliseconds())
		if err != nil {
			continue
		}
		resp.Body.Close()
		return model.Endpoint{
			Name:       host,
			URL:        url,
			Reachable:  true,
			ResponseMs: intptr(ms),
			StatusCode: intptr(resp.StatusCode),
		}
	}
	return model.Endpoint{Name: host, URL: "https://" + host + "/", Reachable: false}
}
