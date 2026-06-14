//go:build linux

// Linux resource discovery for the snapshot's containers/databases/endpoints
// fields. Everything here is read-only and self-contained: the agent
// discovers what's running by reading what is *already on the box* (the Docker
// socket, /proc, well-known data directories, and the local web-server config)
// and never takes configuration, credentials, or external input.
package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
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
		ID      string       `json:"Id"`
		Names   []string     `json:"Names"`
		Image   string       `json:"Image"`
		State   string       `json:"State"`
		Ports   []dockerPort `json:"Ports"`
		Created int64        `json:"Created"`
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
		out[i] = model.Container{
			Name:      name,
			Image:     c.Image,
			State:     c.State,
			Ports:     formatDockerPorts(c.Ports),
			CreatedAt: unixTimePtr(c.Created),
		}
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			mergeContainerInspect(client, id, &out[idx])
		}(i, c.ID)
	}
	wg.Wait()

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

type dockerPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

func mergeContainerInspect(client *http.Client, id string, c *model.Container) {
	resp, err := client.Get("http://docker/v1.41/containers/" + id + "/json")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var raw struct {
		Created      string `json:"Created"`
		RestartCount int    `json:"RestartCount"`
		State        struct {
			Status     string `json:"Status"`
			OOMKilled  bool   `json:"OOMKilled"`
			Dead       bool   `json:"Dead"`
			ExitCode   int    `json:"ExitCode"`
			Error      string `json:"Error"`
			StartedAt  string `json:"StartedAt"`
			FinishedAt string `json:"FinishedAt"`
			Health     *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return
	}

	if raw.State.Status != "" {
		c.State = raw.State.Status
	}
	if raw.State.Health != nil && raw.State.Health.Status != "" {
		c.Health = strptr(raw.State.Health.Status)
	}
	c.RestartCount = intptr(raw.RestartCount)
	c.OOMKilled = boolptr(raw.State.OOMKilled)
	c.ExitCode = intptr(raw.State.ExitCode)
	if raw.State.Error != "" {
		c.Error = strptr(raw.State.Error)
	}
	if t := parseDockerTime(raw.Created); t != nil {
		c.CreatedAt = t
	}
	c.StartedAt = parseDockerTime(raw.State.StartedAt)
	c.FinishedAt = parseDockerTime(raw.State.FinishedAt)
	if len(c.Ports) == 0 {
		c.Ports = formatInspectPorts(raw.NetworkSettings.Ports)
	}
	if raw.State.Dead {
		c.State = "dead"
	}
}

func formatDockerPorts(ports []dockerPort) []string {
	var out []string
	for _, p := range ports {
		if p.PrivatePort == 0 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		private := strconv.Itoa(p.PrivatePort) + "/" + proto
		if p.PublicPort > 0 {
			out = append(out, strconv.Itoa(p.PublicPort)+"->"+private)
		} else {
			out = append(out, private)
		}
	}
	sort.Strings(out)
	return out
}

func formatInspectPorts(ports map[string][]struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}) []string {
	var out []string
	for private, bindings := range ports {
		if len(bindings) == 0 {
			out = append(out, private)
			continue
		}
		for _, b := range bindings {
			if b.HostPort == "" {
				out = append(out, private)
			} else {
				out = append(out, b.HostPort+"->"+private)
			}
		}
	}
	sort.Strings(out)
	return out
}

func unixTimePtr(sec int64) *time.Time {
	if sec <= 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

func parseDockerTime(raw string) *time.Time {
	if raw == "" || strings.HasPrefix(raw, "0001-01-01") {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// ---------------------------------------------------------------------------
// Databases — detect engines from the process table, size from data dirs.
// No connection and no credentials: only the filesystem and /proc are read.
// ---------------------------------------------------------------------------

type dbEngine struct {
	engine   string
	procs    []string // process comm names that indicate this engine
	dataDirs []string // candidate data directories
}

var knownDBs = []dbEngine{
	{engine: "PostgreSQL", procs: []string{"postgres"}, dataDirs: []string{"/var/lib/postgresql", "/var/lib/pgsql"}},
}

// readDatabases reports supported database sources the agent can discover.
// PostgreSQL is detected from local process/data-dir evidence. MSSQL-in-Docker
// is identified automatically; per-database inventory is attempted only when
// the user has explicitly stored read-only SQL credentials in the local agent.
func readDatabases(stateDir string, containers []model.Container) []model.Database {
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

		status := "stopped"
		if running {
			status = "running"
		}

		var size *float64
		if dataDir != "" {
			size = dirSizeGB(dataDir)
		}
		out = append(out, model.Database{
			Name:   "PostgreSQL cluster",
			Engine: db.engine,
			SizeGB: size,
			Status: status,
		})
	}

	mssqlConfigs := loadMSSQLConfigMap(stateDir)
	for _, c := range containers {
		if isMSSQLContainer(c) {
			if cfg, ok := mssqlConfigs[c.Name]; ok {
				if dbs, err := readMSSQLContainerDatabases(c.Name, cfg.Username, cfg.Password); err == nil {
					out = append(out, dbs...)
					continue
				}
			}
			out = append(out, mssqlContainerPlaceholder(c))
		}
	}

	if out == nil {
		return []model.Database{}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func loadMSSQLConfigMap(stateDir string) map[string]agentconfig.MSSQLInventory {
	configs, err := agentconfig.LoadMSSQLInventories(stateDir)
	if err != nil || len(configs) == 0 {
		return nil
	}
	out := make(map[string]agentconfig.MSSQLInventory, len(configs))
	for _, cfg := range configs {
		out[cfg.Container] = cfg
	}
	return out
}

func mssqlContainerPlaceholder(c model.Container) model.Database {
	return model.Database{
		Name:   c.Name,
		Engine: "MSSQL (Docker)",
		SizeGB: nil,
		Status: c.State,
	}
}

func readMSSQLContainerDatabases(container, username, password string) ([]model.Database, error) {
	output, err := dockerExec(container, []string{
		"sh", "-lc", mssqlSQLCmdShell(username),
	}, []string{"SQLCMDPASSWORD=" + password})
	if err != nil {
		return nil, err
	}
	dbs := parseMSSQLInventory(output)
	if len(dbs) == 0 {
		return nil, errors.New("mssql inventory returned no databases")
	}
	return dbs, nil
}

func mssqlSQLCmdShell(username string) string {
	query := `SET NOCOUNT ON; SELECT DB_NAME(database_id) + '|' + CONVERT(varchar(32), CAST(SUM(size) * 8.0 / 1024 / 1024 AS decimal(18,3))) FROM sys.master_files WHERE database_id > 4 GROUP BY database_id ORDER BY DB_NAME(database_id);`
	return fmt.Sprintf(`SQLCMD="$(command -v sqlcmd || command -v /opt/mssql-tools18/bin/sqlcmd || command -v /opt/mssql-tools/bin/sqlcmd)" && "$SQLCMD" -S localhost -U %s -C -b -l 5 -h -1 -W -Q %s`,
		shellQuote(username),
		shellQuote(query),
	)
}

func parseMSSQLInventory(output []byte) []model.Database {
	var out []model.Database
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "(") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		sizeRaw := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}
		var size *float64
		if parsed, err := strconv.ParseFloat(sizeRaw, 64); err == nil {
			size = f64ptr(parsed)
		}
		out = append(out, model.Database{
			Name:   name,
			Engine: "MSSQL",
			SizeGB: size,
			Status: "running",
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func TestMSSQLInventory(container, username, password string) ([]model.Database, error) {
	return readMSSQLContainerDatabases(container, username, password)
}

func dockerExec(container string, cmd []string, env []string) ([]byte, error) {
	client := dockerHTTP()
	body := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Tty":          false,
		"Cmd":          cmd,
		"Env":          env,
	}
	payload, _ := json.Marshal(body)
	resp, err := client.Post("http://docker/v1.41/containers/"+container+"/exec", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("docker exec create failed: %s", resp.Status)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("docker exec create returned empty id")
	}

	startPayload := []byte(`{"Detach":false,"Tty":false}`)
	startResp, err := client.Post("http://docker/v1.41/exec/"+created.ID+"/start", "application/json", bytes.NewReader(startPayload))
	if err != nil {
		return nil, err
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker exec start failed: %s", startResp.Status)
	}
	raw, err := io.ReadAll(startResp.Body)
	if err != nil {
		return nil, err
	}
	return dockerDemux(raw), nil
}

func dockerDemux(raw []byte) []byte {
	var out bytes.Buffer
	for len(raw) >= 8 {
		size := int(binary.BigEndian.Uint32(raw[4:8]))
		raw = raw[8:]
		if size < 0 || size > len(raw) {
			break
		}
		out.Write(raw[:size])
		raw = raw[size:]
	}
	if out.Len() > 0 {
		return out.Bytes()
	}
	return raw
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isMSSQLContainer(c model.Container) bool {
	s := strings.ToLower(c.Name + " " + c.Image)
	return strings.Contains(s, "mssql") ||
		strings.Contains(s, "sqlserver") ||
		strings.Contains(s, "sql-server") ||
		strings.Contains(s, "azure-sql-edge")
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

// dirSizeGB sums the apparent size of all files under root and returns it
// rounded, or nil if root itself can't be accessed (e.g. permission
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
// configuration. Free v1 reports names only; outside reachability belongs to
// Pro relay checks.
// ---------------------------------------------------------------------------

const maxEndpoints = 25

func readEndpoints() []model.Endpoint {
	hosts := discoverHostnames()
	if len(hosts) == 0 {
		return []model.Endpoint{}
	}

	out := make([]model.Endpoint, len(hosts))
	for i, h := range hosts {
		source := endpointSources[h]
		out[i] = model.Endpoint{Name: h, Source: strptr(source)}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

var endpointSources = map[string]string{}

// discoverHostnames reads nginx, Apache, and Caddy config to collect the
// hostnames this server is configured to serve.
func discoverHostnames() []string {
	set := map[string]bool{}
	endpointSources = map[string]string{}

	for _, dir := range []string{"/etc/nginx/sites-enabled", "/etc/nginx/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "server_name", "nginx", set)
		}
	}
	for _, dir := range []string{"/etc/apache2/sites-enabled", "/etc/httpd/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "servername", "apache", set)
			scanDirective(f, "serveralias", "apache", set)
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
func scanDirective(path, directive, source string, set map[string]bool) {
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
			endpointSources[strings.ToLower(tok)] = source
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
			endpointSources[strings.ToLower(tok)] = "caddy"
		}
	}
}

// validHost rejects wildcards, catch-alls, and anything that isn't a plausible
// DNS hostname so the app never displays a junk target.
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
