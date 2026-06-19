// Command meerkat-agent is the open-source Meerkat monitoring agent.
//
// Usage:
//
//	meerkat-agent once                collect one snapshot and print JSON
//	meerkat-agent serve [--addr][--dir] serve GET /v1/status over HTTPS
//	meerkat-agent relay               push snapshots outbound to Meerkat relay
//	meerkat-agent gen-cert [--dir]    generate the TLS cert/key if absent (install)
//	meerkat-agent gen-token [--dir]   generate the bearer token if absent (install)
//	meerkat-agent rotate-token [--dir][--addr] replace token and print enrollment
//	meerkat-agent rotate-cert [--dir][--addr] replace TLS cert/key and print enrollment
//	meerkat-agent fingerprint [--dir] print the TLS cert fingerprint
//	meerkat-agent enroll [--dir][--addr] print the app enrollment details
//	meerkat-agent config relay set [--dir][--enrollment-code]
//	meerkat-agent config remove-mssql [--dir] <container>
//	meerkat-agent version             print the agent version
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
	agentconfig "github.com/AndiOliverIon/meerkat-agent/internal/config"
	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
	"github.com/AndiOliverIon/meerkat-agent/internal/relay"
	"github.com/AndiOliverIon/meerkat-agent/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "once":
		snap := collect.New(identity.DefaultDir).Once()
		out, err := json.MarshalIndent(snap, "", "  ")
		if err != nil {
			fatal("once:", err)
		}
		fmt.Println(string(out))

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", ":8765", "address to listen on")
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
		_ = fs.Parse(os.Args[2:])
		srv, err := server.New(*addr, collect.New(*dir), *dir)
		if err != nil {
			fatal("serve:", err, "\nhint: run `meerkat-agent gen-cert` and `gen-token` first (the package does this on install)")
		}
		if err := srv.Run(); err != nil {
			fatal("serve:", err)
		}

	case "relay":
		fs := flag.NewFlagSet("relay", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity/config dir")
		backendURL := fs.String("backend-url", "", "Meerkat backend base URL")
		serverID := fs.String("server-id", "", "Meerkat backend server id")
		userProfileID := fs.String("user-profile-id", "", "optional backend owner profile id metadata")
		relayToken := fs.String("relay-token", "", "Meerkat relay bearer token")
		_ = fs.Parse(os.Args[2:])
		cfg, err := agentconfig.LoadRelayConfig(*dir)
		if err != nil && !errors.Is(err, agentconfig.ErrRelayConfigNotFound) {
			fatal("relay:", err)
		}
		if *backendURL != "" {
			cfg.BackendURL = *backendURL
		}
		if *serverID != "" {
			cfg.ServerID = *serverID
		}
		if *userProfileID != "" {
			cfg.UserProfileID = *userProfileID
		}
		if *relayToken != "" {
			cfg.RelayToken = *relayToken
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		runner := relay.Runner{
			BackendURL:          cfg.BackendURL,
			ServerID:            cfg.ServerID,
			UserProfileID:       cfg.UserProfileID,
			RelayToken:          cfg.RelayToken,
			RelayTokenExpiresAt: cfg.RelayTokenExpiresAt,
			Collector:           collect.New(*dir),
		}
		if err := runner.Run(ctx); err != nil {
			fatal("relay:", err)
		}

	case "gen-cert":
		dir := dirFlag("gen-cert")
		created, err := identity.GenerateCert(dir)
		if err != nil {
			fatal("gen-cert:", err)
		}
		if created {
			fmt.Println("meerkat-agent: generated TLS cert in", dir)
		} else {
			fmt.Println("meerkat-agent: TLS cert already present in", dir, "(left unchanged)")
		}

	case "gen-token":
		dir := dirFlag("gen-token")
		_, created, err := identity.GenerateToken(dir)
		if err != nil {
			fatal("gen-token:", err)
		}
		if created {
			fmt.Println("meerkat-agent: generated bearer token in", dir)
		} else {
			fmt.Println("meerkat-agent: bearer token already present in", dir, "(left unchanged)")
		}

	case "rotate-token":
		fs := flag.NewFlagSet("rotate-token", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
		addr := fs.String("addr", "", "public address the app should use (host:port or https://host:port)")
		_ = fs.Parse(os.Args[2:])
		if _, err := identity.RotateToken(*dir); err != nil {
			fatal("rotate-token:", err)
		}
		if err := printEnrollment(*dir, *addr); err != nil {
			fatal("rotate-token:", err)
		}

	case "rotate-cert":
		fs := flag.NewFlagSet("rotate-cert", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
		addr := fs.String("addr", "", "public address the app should use (host:port or https://host:port)")
		_ = fs.Parse(os.Args[2:])
		if err := identity.RotateCert(*dir); err != nil {
			fatal("rotate-cert:", err)
		}
		if err := printEnrollment(*dir, *addr); err != nil {
			fatal("rotate-cert:", err)
		}
		fmt.Fprintln(os.Stderr, "\nRestart meerkat-agent for the new certificate to be served:")
		fmt.Fprintln(os.Stderr, "  sudo systemctl restart meerkat-agent")

	case "fingerprint":
		dir := dirFlag("fingerprint")
		fp, err := identity.Fingerprint(dir)
		if err != nil {
			fatal("fingerprint:", err)
		}
		fmt.Println(fp)

	case "enroll":
		fs := flag.NewFlagSet("enroll", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
		addr := fs.String("addr", "", "public address the app should use (host:port); auto-detected if empty")
		_ = fs.Parse(os.Args[2:])
		if err := printEnrollment(*dir, *addr); err != nil {
			fatal("enroll:", err)
		}

	case "config":
		configCommand(os.Args[2:])

	case "version", "-v", "--version":
		fmt.Println("meerkat-agent", collect.Version)

	default:
		usage()
		os.Exit(2)
	}
}

func configCommand(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "relay":
		relayConfigCommand(args[1:])

	case "remove-mssql":
		fs := flag.NewFlagSet("config remove-mssql", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token/config)")
		_ = fs.Parse(args[1:])
		if fs.NArg() != 1 {
			fatal("config remove-mssql: container is required")
		}
		container := strings.TrimSpace(fs.Arg(0))
		if err := agentconfig.RemoveMSSQLInventory(*dir, container); err != nil {
			if errors.Is(err, agentconfig.ErrMSSQLInventoryNotFound) {
				fatal("config remove-mssql:", container, "is not configured")
			}
			fatal("config remove-mssql:", err)
		}
		fmt.Println("meerkat-agent: removed MSSQL inventory config for", container)

	default:
		usage()
		os.Exit(2)
	}
}

func relayConfigCommand(args []string) {
	if len(args) < 1 {
		usage()
		os.Exit(2)
	}
	switch args[0] {
	case "set":
		fs := flag.NewFlagSet("config relay set", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token/config)")
		enrollmentCode := fs.String("enrollment-code", "", "Meerkat relay enrollment code")
		backendURL := fs.String("backend-url", "", "Meerkat backend base URL")
		serverID := fs.String("server-id", "", "Meerkat backend server id")
		userProfileID := fs.String("user-profile-id", "", "optional backend owner profile id metadata")
		relayToken := fs.String("relay-token", "", "Meerkat relay bearer token")
		_ = fs.Parse(args[1:])
		var cfg agentconfig.RelayConfig
		if strings.TrimSpace(*enrollmentCode) != "" {
			fingerprint := ""
			if fp, err := identity.Fingerprint(*dir); err == nil {
				fingerprint = fp
			}
			var err error
			cfg, err = relay.ConsumeEnrollmentCode(context.Background(), *enrollmentCode, fingerprint, collect.Version, nil)
			if err != nil {
				fatal("config relay set:", err)
			}
		} else {
			cfg = agentconfig.RelayConfig{
				BackendURL:    *backendURL,
				ServerID:      *serverID,
				UserProfileID: *userProfileID,
				RelayToken:    *relayToken,
			}
		}
		if err := agentconfig.SaveRelayConfig(*dir, cfg); err != nil {
			fatal("config relay set:", err)
		}
		fmt.Println("meerkat-agent: saved relay config to", agentconfig.RelayConfigPath(*dir))
		fmt.Println("meerkat-agent: enable relay mode with:")
		fmt.Println("  sudo systemctl enable meerkat-agent-relay && sudo systemctl restart meerkat-agent-relay")

	case "show":
		fs := flag.NewFlagSet("config relay show", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token/config)")
		_ = fs.Parse(args[1:])
		cfg, err := agentconfig.LoadRelayConfig(*dir)
		if err != nil {
			if errors.Is(err, agentconfig.ErrRelayConfigNotFound) {
				fatal("config relay show: relay config is not configured")
			}
			fatal("config relay show:", err)
		}
		out, err := json.MarshalIndent(relayConfigForDisplay(cfg), "", "  ")
		if err != nil {
			fatal("config relay show:", err)
		}
		fmt.Println(string(out))

	case "remove":
		fs := flag.NewFlagSet("config relay remove", flag.ExitOnError)
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token/config)")
		_ = fs.Parse(args[1:])
		if err := agentconfig.RemoveRelayConfig(*dir); err != nil {
			if errors.Is(err, agentconfig.ErrRelayConfigNotFound) {
				fatal("config relay remove: relay config is not configured")
			}
			fatal("config relay remove:", err)
		}
		fmt.Println("meerkat-agent: removed relay config from", agentconfig.RelayConfigPath(*dir))

	default:
		usage()
		os.Exit(2)
	}
}

func relayConfigForDisplay(cfg agentconfig.RelayConfig) agentconfig.RelayConfig {
	if strings.TrimSpace(cfg.RelayToken) != "" {
		cfg.RelayToken = "redacted"
	}
	return cfg
}

// printEnrollment prints the copy-paste details the user enters in the app:
// address, cert fingerprint, and token — plus a single-line base64 code.
func printEnrollment(dir, addr string) error {
	fp, err := identity.Fingerprint(dir)
	if err != nil {
		return err
	}
	token, err := identity.LoadToken(dir)
	if err != nil {
		return err
	}
	if addr == "" {
		addr = identity.PrimaryIP() + ":8765"
	}
	addr = normalizeAddress(addr)

	code := base64.RawURLEncoding.EncodeToString(mustJSON(map[string]string{
		"address": addr, "fingerprint": fp, "token": token,
	}))
	onePaste := "address=" + addr + " token=" + token + " fingerprint=" + fp

	fmt.Println(strings.TrimSpace(`
Meerkat agent — add this server in the app:

  Address:      ` + addr + `
  Fingerprint:  ` + fp + `
  Token:        ` + token + `

One-paste line:
  ` + onePaste + `

One-paste code (JSON base64):
  ` + code + `

(If the address is wrong, pass --addr host:port. Make sure that address is
reachable from your phone — open the port in your firewall/security group.)`))
	return nil
}

func normalizeAddress(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "https://" + addr
}

func dirFlag(cmd string) string {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
	_ = fs.Parse(os.Args[2:])
	return *dir
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"meerkat-agent:"}, args...)...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintln(os.Stderr, `meerkat-agent — Meerkat monitoring agent

usage:
  meerkat-agent once                 collect one snapshot and print JSON
  meerkat-agent serve [--addr][--dir] serve GET /v1/status over HTTPS (default :8765)
  meerkat-agent relay --backend-url URL --server-id ID --relay-token TOKEN [--dir] push snapshots to Meerkat relay
  meerkat-agent gen-cert [--dir]     generate the TLS cert/key if absent (install hook)
  meerkat-agent gen-token [--dir]    generate the bearer token if absent (install hook)
  meerkat-agent rotate-token [--dir][--addr] replace token and print enrollment details
  meerkat-agent rotate-cert [--dir][--addr] replace TLS cert/key and print enrollment details
  meerkat-agent fingerprint [--dir]  print the TLS cert fingerprint
  meerkat-agent enroll [--dir][--addr] print the app enrollment details
  meerkat-agent config relay set --enrollment-code CODE [--dir]
  meerkat-agent config relay set --backend-url URL --server-id ID --relay-token TOKEN [--dir]
  meerkat-agent config relay show [--dir]
  meerkat-agent config relay remove [--dir]
  meerkat-agent config remove-mssql [--dir] <container>
  meerkat-agent version              print version`)
}
