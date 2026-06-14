// Command meerkat-agent is the open-source Meerkat monitoring agent.
//
// Usage:
//
//	meerkat-agent once                collect one snapshot and print JSON
//	meerkat-agent serve [--addr][--dir] serve GET /v1/status over HTTPS
//	meerkat-agent gen-cert [--dir]    generate the TLS cert/key if absent (install)
//	meerkat-agent gen-token [--dir]   generate the bearer token if absent (install)
//	meerkat-agent rotate-token [--dir][--addr] replace token and print enrollment
//	meerkat-agent fingerprint [--dir] print the TLS cert fingerprint
//	meerkat-agent enroll [--dir][--addr] print the app enrollment details
//	meerkat-agent version             print the agent version
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/AndiOliverIon/meerkat-agent/internal/collect"
	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
	"github.com/AndiOliverIon/meerkat-agent/internal/server"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "once":
		snap := collect.New().Once()
		out, _ := json.MarshalIndent(snap, "", "  ")
		fmt.Println(string(out))

	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", ":8765", "address to listen on")
		dir := fs.String("dir", identity.DefaultDir, "identity dir (cert/key/token)")
		_ = fs.Parse(os.Args[2:])
		srv, err := server.New(*addr, collect.New(), *dir)
		if err != nil {
			fatal("serve:", err, "\nhint: run `meerkat-agent gen-cert` and `gen-token` first (the package does this on install)")
		}
		if err := srv.Run(); err != nil {
			fatal("serve:", err)
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

	case "version", "-v", "--version":
		fmt.Println("meerkat-agent", collect.Version)

	default:
		usage()
		os.Exit(2)
	}
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
  meerkat-agent gen-cert [--dir]     generate the TLS cert/key if absent (install hook)
  meerkat-agent gen-token [--dir]    generate the bearer token if absent (install hook)
  meerkat-agent rotate-token [--dir][--addr] replace token and print enrollment details
  meerkat-agent fingerprint [--dir]  print the TLS cert fingerprint
  meerkat-agent enroll [--dir][--addr] print the app enrollment details
  meerkat-agent version              print version`)
}
