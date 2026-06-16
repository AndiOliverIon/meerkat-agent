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
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

// ---------------------------------------------------------------------------
// Containers — read from the Docker Engine API over its unix socket.
// ---------------------------------------------------------------------------

const dockerSocket = "/var/run/docker.sock"
const dockerInspectConcurrency = 8
const dockerHTTPTimeout = 4 * time.Second
const dockerExecStartTimeout = 8 * time.Second
const dockerExecOutputLimit = 1 << 20
const dirSizeCacheTTL = 60 * time.Second

var dirSizeCache = struct {
	sync.Mutex
	values map[string]dirSizeCacheEntry
}{values: map[string]dirSizeCacheEntry{}}

type dirSizeCacheEntry struct {
	at    time.Time
	value *float64
}

// dockerHTTP returns an http.Client that speaks to the Docker socket. The host
// part of the URL is irrelevant; the dialer always connects to the socket.
func dockerHTTP() *http.Client {
	return dockerHTTPWithTimeout(dockerHTTPTimeout)
}

func dockerHTTPWithTimeout(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
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
	sem := make(chan struct{}, dockerInspectConcurrency)
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
		sem <- struct{}{}
		wg.Add(1)
		go func(idx int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
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
			Running    bool   `json:"Running"`
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
	if !raw.State.Running || raw.State.OOMKilled {
		c.OOMKilled = boolptr(raw.State.OOMKilled)
	}
	if !raw.State.Running {
		c.ExitCode = intptr(raw.State.ExitCode)
	}
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
		if db.engine == "PostgreSQL" {
			out = append(out, readPostgreSQLDatabases(db, procs)...)
		}
	}

	mssqlConfigs := loadMSSQLConfigMap(stateDir)
	for _, c := range containers {
		if isMSSQLContainer(c) {
			fileDBs := readMSSQLFileDatabases(c.Name)
			if cfg, ok := mssqlConfigs[c.Name]; ok {
				if dbs, err := readMSSQLContainerDatabases(c.Name, cfg.Username, cfg.Password); err == nil {
					out = append(out, mergeMSSQLDatabaseInventory(dbs, fileDBs)...)
					continue
				} else {
					log.Printf("mssql inventory for container %q failed: %v", c.Name, err)
				}
			}
			if len(fileDBs) > 0 {
				out = append(out, fileDBs...)
				continue
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

func readPostgreSQLDatabases(db dbEngine, procs map[string]bool) []model.Database {
	running := false
	for _, p := range db.procs {
		if procs[p] {
			running = true
			break
		}
	}
	dataDirs := postgresDataDirs(db.dataDirs)
	if !running && len(dataDirs) == 0 {
		return nil // engine neither running nor installed
	}

	if running {
		if dbs := readPostgreSQLLocalDatabases(); len(dbs) > 0 {
			return dbs
		}
	}

	status := "stopped"
	if running {
		status = "running"
	}

	var out []model.Database
	for _, dataDir := range dataDirs {
		out = append(out, model.Database{
			Name:   postgresClusterName(dataDir),
			Engine: db.engine,
			SizeGB: dirSizeGB(dataDir),
			Status: status,
		})
	}
	return out
}

func readPostgreSQLLocalDatabases() []model.Database {
	psql, err := exec.LookPath("psql")
	if err != nil {
		return nil
	}

	query := `SELECT datname || '|' || pg_database_size(datname) FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname;`
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, psql, "-AtX", "-d", "postgres", "-c", query)
	cmd.Env = append(os.Environ(), "PGCONNECT_TIMEOUT=3")
	output, err := cmd.Output()
	if err != nil || ctx.Err() != nil {
		return nil
	}
	return parsePostgreSQLInventory(output)
}

func parsePostgreSQLInventory(output []byte) []model.Database {
	var out []model.Database
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
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
		if bytes, err := strconv.ParseFloat(sizeRaw, 64); err == nil {
			size = f64ptr(round1(bytes / 1e9))
		}
		out = append(out, model.Database{
			Name:   name,
			Engine: "PostgreSQL",
			SizeGB: size,
			Status: "running",
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func postgresDataDirs(roots []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		for _, dir := range postgresDataDirsUnder(root, 0) {
			if !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	sort.Strings(out)
	return out
}

func postgresDataDirsUnder(root string, depth int) []string {
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil
	}
	if _, err := os.Stat(filepath.Join(root, "PG_VERSION")); err == nil {
		return []string{root}
	}
	if depth >= 3 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, postgresDataDirsUnder(filepath.Join(root, entry.Name()), depth+1)...)
		}
	}
	return out
}

func postgresClusterName(dataDir string) string {
	clean := filepath.Clean(dataDir)
	version := filepath.Base(filepath.Dir(clean))
	cluster := filepath.Base(clean)
	if version != "." && version != string(filepath.Separator) && version != "" && version != "pgsql" && version != "postgresql" {
		return "PostgreSQL " + version + "/" + cluster + " cluster"
	}
	return "PostgreSQL " + cluster + " cluster"
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

type dockerMount struct {
	Type        string `json:"Type"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

func readMSSQLFileDatabases(container string) []model.Database {
	var out []model.Database
	for _, dir := range mssqlDataDirs(container) {
		out = append(out, scanMSSQLDataDir(dir)...)
	}
	return out
}

func mssqlDataDirs(container string) []string {
	client := dockerHTTP()
	escapedContainer := url.PathEscape(container)
	resp, err := client.Get("http://docker/v1.41/containers/" + escapedContainer + "/json")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var raw struct {
		Mounts []dockerMount `json:"Mounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil
	}

	var dirs []string
	for _, m := range raw.Mounts {
		if m.Source == "" {
			continue
		}
		if filepath.Clean(m.Destination) == "/var/opt/mssql/data" {
			dirs = append(dirs, m.Source)
		}
	}
	sort.Strings(dirs)
	return dirs
}

func scanMSSQLDataDir(root string) []model.Database {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	sizes := map[string]int64{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".mdf" && ext != ".ndf" && ext != ".ldf" {
			continue
		}
		if isMSSQLSystemDatabaseFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := mssqlDatabaseNameFromFile(entry.Name())
		if name == "" || isMSSQLSystemDatabase(name) {
			continue
		}
		sizes[name] += fileDiskBytes(info)
	}

	out := make([]model.Database, 0, len(sizes))
	for name, bytes := range sizes {
		size := round1(float64(bytes) / 1e9)
		out = append(out, model.Database{
			Name:   name,
			Engine: "MSSQL",
			SizeGB: &size,
			Status: "detected",
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func mssqlDatabaseNameFromFile(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	lower := strings.ToLower(stem)

	switch lower {
	case "mastlog":
		return "master"
	case "modellog":
		return "model"
	case "msdblog", "msdbdata":
		return "msdb"
	case "templog":
		return "tempdb"
	}

	if ext == ".ldf" && strings.HasSuffix(lower, "_log") {
		return stem[:len(stem)-len("_log")]
	}
	return stem
}

func isMSSQLSystemDatabaseFile(filename string) bool {
	stem := strings.ToLower(strings.TrimSuffix(filename, filepath.Ext(filename)))
	base := strings.TrimSuffix(stem, "_log")

	switch base {
	case "master", "mastlog", "model", "modellog", "msdbdata", "msdblog", "tempdb", "templog":
		return true
	}
	return strings.HasPrefix(base, "model_msdb") || strings.HasPrefix(base, "model_replicatedmaster")
}

func fileDiskBytes(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Blocks > 0 {
		return st.Blocks * 512
	}
	return info.Size()
}

func mergeMSSQLDatabaseInventory(primary, fallback []model.Database) []model.Database {
	if len(fallback) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary))
	for _, db := range primary {
		seen[strings.ToLower(db.Name)] = true
	}
	out := append([]model.Database{}, primary...)
	for _, db := range fallback {
		if !seen[strings.ToLower(db.Name)] {
			out = append(out, db)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func readMSSQLContainerDatabases(container, username, password string) ([]model.Database, error) {
	output, err := dockerExec(container, []string{
		"sh", "-c", mssqlSQLCmdShell(username, mssqlMetadataInventoryQuery),
	}, []string{"SQLCMDPASSWORD=" + password})
	if err != nil {
		if fallback, fallbackErr := readMSSQLContainerDatabaseSizes(container, username, password); fallbackErr == nil {
			log.Printf("mssql metadata inventory for container %q failed, using size-only inventory: %v", container, err)
			return fallback, nil
		}
		return nil, err
	}
	dbs := parseMSSQLInventory(output)
	if len(dbs) == 0 {
		if fallback, fallbackErr := readMSSQLContainerDatabaseSizes(container, username, password); fallbackErr == nil {
			log.Printf("mssql metadata inventory for container %q returned no parseable rows, using size-only inventory", container)
			return fallback, nil
		}
		return nil, errors.New("mssql inventory returned no databases")
	}
	return dbs, nil
}

func readMSSQLContainerDatabaseSizes(container, username, password string) ([]model.Database, error) {
	output, err := dockerExec(container, []string{
		"sh", "-c", mssqlSQLCmdShell(username, mssqlSizeInventoryQuery),
	}, []string{"SQLCMDPASSWORD=" + password})
	if err != nil {
		return nil, err
	}
	dbs := parseMSSQLInventory(output)
	if len(dbs) == 0 {
		return nil, errors.New("mssql size inventory returned no databases")
	}
	return dbs, nil
}

const mssqlMetadataInventoryQuery = `SET NOCOUNT ON; SELECT CONCAT(d.name, '|', CONVERT(varchar(32), CAST(SUM(mf.size) * 8.0 / 1024 / 1024 AS decimal(18,3))), '|', COALESCE(d.state_desc, ''), '|', COALESCE(d.recovery_model_desc, ''), '|', CONVERT(varchar(33), d.create_date, 126)) FROM sys.databases d JOIN sys.master_files mf ON mf.database_id = d.database_id WHERE d.database_id > 4 GROUP BY d.name, d.state_desc, d.recovery_model_desc, d.create_date ORDER BY d.name;`

const mssqlSizeInventoryQuery = `SET NOCOUNT ON; SELECT DB_NAME(database_id) + '|' + CONVERT(varchar(32), CAST(SUM(size) * 8.0 / 1024 / 1024 AS decimal(18,3))) FROM sys.master_files WHERE database_id > 4 GROUP BY database_id ORDER BY DB_NAME(database_id);`

func mssqlSQLCmdShell(username string, query string) string {
	return fmt.Sprintf(`SQLCMD="$(command -v sqlcmd || command -v /opt/mssql-tools18/bin/sqlcmd || command -v /opt/mssql-tools/bin/sqlcmd)" && "$SQLCMD" -S localhost -U %s -C -b -l 5 -h -1 -W -w 65535 -Q %s`,
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
		if len(parts) != 2 && len(parts) != 5 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		sizeRaw := strings.TrimSpace(parts[1])
		if name == "" || isMSSQLSystemDatabase(name) {
			continue
		}
		var size *float64
		if parsed, err := strconv.ParseFloat(sizeRaw, 64); err == nil {
			size = f64ptr(parsed)
		}
		db := model.Database{
			Name:   name,
			Engine: "MSSQL",
			SizeGB: size,
			Status: "running",
		}
		if len(parts) == 5 {
			db.State = strptr(strings.TrimSpace(parts[2]))
			db.RecoveryModel = strptr(strings.TrimSpace(parts[3]))
			db.CreatedAt = parseMSSQLDate(strings.TrimSpace(parts[4]))
		}
		out = append(out, db)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

func parseMSSQLDate(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	layouts := []string{
		"2006-01-02T15:04:05.9999999",
		"2006-01-02T15:04:05.999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			t := parsed.UTC()
			return &t
		}
	}
	return nil
}

func isMSSQLSystemDatabase(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "master", "model", "msdb", "tempdb":
		return true
	default:
		return false
	}
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
	escapedContainer := url.PathEscape(container)
	resp, err := client.Post("http://docker/v1.41/containers/"+escapedContainer+"/exec", "application/json", bytes.NewReader(payload))
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
	startClient := dockerHTTPWithTimeout(dockerExecStartTimeout)
	startResp, err := startClient.Post("http://docker/v1.41/exec/"+created.ID+"/start", "application/json", bytes.NewReader(startPayload))
	if err != nil {
		return nil, err
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker exec start failed: %s", startResp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(startResp.Body, dockerExecOutputLimit))
	if err != nil {
		return nil, err
	}
	return dockerDemux(raw), nil
}

func dockerDemux(raw []byte) []byte {
	original := raw
	var out bytes.Buffer
	for len(raw) >= 8 {
		if (raw[0] != 1 && raw[0] != 2) || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 {
			if out.Len() == 0 {
				return original
			}
			break
		}
		size := int(binary.BigEndian.Uint32(raw[4:8]))
		raw = raw[8:]
		if size <= 0 || size > len(raw) {
			break
		}
		out.Write(raw[:size])
		raw = raw[size:]
	}
	if out.Len() > 0 {
		return out.Bytes()
	}
	return original
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isMSSQLContainer(c model.Container) bool {
	return isMSSQLName(c.Name) || isMSSQLImage(c.Image)
}

func isMSSQLName(name string) bool {
	name = strings.Trim(strings.ToLower(name), "/")
	switch name {
	case "mssql", "sqlserver", "sql-server", "mssql-server", "azure-sql-edge":
		return true
	}
	for _, part := range splitContainerRef(name) {
		if part == "mssql" || part == "sqlserver" {
			return true
		}
	}
	return false
}

func isMSSQLImage(image string) bool {
	for _, segment := range splitImageRef(image) {
		switch segment {
		case "mssql", "sqlserver", "sql-server", "azure-sql-edge":
			return true
		}
	}
	return false
}

func splitContainerRef(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == '/' || r == ':' || r == '@' || r == '.' || r == '_' || r == '-'
	})
}

func splitImageRef(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return r == '/' || r == ':' || r == '@'
	})
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

// dirSizeGB sums the physical allocation of files under root and returns it
// rounded, or nil if root itself can't be accessed (e.g. permission denied).
// Individual unreadable entries deeper in the tree are skipped.
func dirSizeGB(root string) *float64 {
	now := time.Now()
	dirSizeCache.Lock()
	if cached, ok := dirSizeCache.values[root]; ok && now.Sub(cached.at) < dirSizeCacheTTL {
		value := cloneFloat64(cached.value)
		dirSizeCache.Unlock()
		return value
	}
	dirSizeCache.Unlock()

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
			total += fileDiskBytes(info)
		}
		return nil
	})
	if !accessible {
		return nil // couldn't read anything under root
	}
	size := f64ptr(round1(float64(total) / 1e9))
	dirSizeCache.Lock()
	dirSizeCache.values[root] = dirSizeCacheEntry{at: now, value: cloneFloat64(size)}
	dirSizeCache.Unlock()
	return size
}

func cloneFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// ---------------------------------------------------------------------------
// Endpoints — discover the hostnames this box serves from its own web-server
// configuration. Free v1 reports names only; outside reachability belongs to
// Pro relay checks.
// ---------------------------------------------------------------------------

const maxEndpoints = 25

func readEndpoints() []model.Endpoint {
	hosts, sources := discoverHostnames()
	if len(hosts) == 0 {
		return []model.Endpoint{}
	}

	out := make([]model.Endpoint, len(hosts))
	for i, h := range hosts {
		source := sources[h]
		out[i] = model.Endpoint{Name: h, Source: strptr(source)}
	}

	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// discoverHostnames reads nginx, Apache, and Caddy config to collect the
// hostnames this server is configured to serve.
func discoverHostnames() ([]string, map[string]string) {
	set := map[string]bool{}
	sources := map[string]string{}

	for _, dir := range []string{"/etc/nginx/sites-enabled", "/etc/nginx/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "server_name", "nginx", set, sources)
		}
	}
	for _, dir := range []string{"/etc/apache2/sites-enabled", "/etc/httpd/conf.d"} {
		for _, f := range listFiles(dir) {
			scanDirective(f, "servername", "apache", set, sources)
			scanDirective(f, "serveralias", "apache", set, sources)
		}
	}
	scanCaddyfile("/etc/caddy/Caddyfile", set, sources)

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
	return out, sources
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
func scanDirective(path, directive, source string, set map[string]bool, sources map[string]string) {
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
			sources[strings.ToLower(tok)] = source
		}
	}
}

// scanCaddyfile collects site addresses from a Caddyfile (lines that open a
// site block, e.g. "example.com, www.example.com {").
func scanCaddyfile(path string, set map[string]bool, sources map[string]string) {
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
			sources[strings.ToLower(tok)] = "caddy"
		}
	}
}

// validHost rejects wildcards, catch-alls, and anything that isn't a plausible
// DNS hostname so the app never displays a junk target.
func validHost(h string) bool {
	if h == "" || h == "_" || strings.HasPrefix(h, "*") || h == "localhost" || strings.HasPrefix(h, "localhost.") {
		return false
	}
	if !strings.Contains(h, ".") {
		return false
	}
	labels := strings.Split(h, ".")
	for _, label := range labels {
		if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
