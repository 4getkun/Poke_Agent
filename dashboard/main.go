//go:build legacy

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"pokegents/dashboard/server"
	"pokegents/dashboard/server/services"
	"pokegents/dashboard/server/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: pokegents-dashboard <command>")
		fmt.Println("Commands:")
		fmt.Println("  serve    Start the dashboard server")
		fmt.Println("  index    Build the search index and exit")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "index":
		runIndex()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runServe(args []string) {
	cfg := server.DefaultConfig()
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	port := flags.Int("port", 0, "dashboard listen port (overrides config/env when non-zero)")
	bindHost := flags.String("bind", "", "dashboard bind host (default 127.0.0.1)")
	webDir := flags.String("web-dir", "", "dashboard web assets directory")
	if err := flags.Parse(args); err != nil {
		log.Fatalf("failed to parse serve flags: %v", err)
	}
	// Override from environment
	if v := os.Getenv("POKEGENTS_DATA"); v != "" {
		cfg.DataDir = v
		cfg.SearchDBPath = filepath.Join(v, "search.db")
	}
	if v := os.Getenv("DASHBOARD_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Port = p
		}
	}
	if v := os.Getenv("POKEGENTS_BIND_HOST"); v != "" {
		cfg.BindHost = v
	}
	if *port > 0 {
		cfg.Port = *port
	}
	if *bindHost != "" {
		cfg.BindHost = *bindHost
	}

	// Find web directory: check relative to binary, then cwd
	for _, candidate := range []string{
		filepath.Join(filepath.Dir(os.Args[0]), "web", "dist"),
		filepath.Join("web", "dist"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			cfg.WebDir = candidate
			break
		}
	}
	if *webDir != "" {
		cfg.WebDir = *webDir
	}

	s, err := server.NewServer(cfg)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	// Trap SIGTERM/SIGINT so we run Server.Stop (which cleanly closes all
	// chat ACP subprocesses + HTTP server). Without this, a `kill` on the
	// dashboard orphans every chat backend and the next dashboard can't
	// re-attach to them via stdio. SIGKILL bypasses this — but our reattach
	// logic on next startup handles that case as well.
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, syscall.SIGTERM, syscall.SIGINT)
	errchan := make(chan error, 1)
	go func() { errchan <- s.Start() }()

	select {
	case sig := <-sigchan:
		log.Printf("received %s, shutting down", sig)
		s.Stop()
	case err := <-errchan:
		s.Stop()
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	}
}

func runIndex() {
	cfg := server.DefaultConfig()
	if v := os.Getenv("POKEGENTS_DATA"); v != "" {
		cfg.DataDir = v
		cfg.SearchDBPath = filepath.Join(v, "search.db")
	}

	state := server.NewStateManager(cfg.DataDir, cfg.ClaudeProjectDir)
	state.LoadAll()

	fs := store.NewFileStore(cfg.DataDir)
	search, err := services.NewSearchService(cfg.SearchDBPath, cfg.ClaudeProjectDir, fs.Profiles,
		services.ProfileMatcherFunc(func(cwd string) string {
			name, _, _ := state.MatchProfile(cwd)
			return name
		}),
	)
	if err != nil {
		log.Fatalf("failed to create search index: %v", err)
	}
	defer search.Close()

	log.Println("Building search index...")
	search.BuildIndex()
	log.Println("Done.")
}
