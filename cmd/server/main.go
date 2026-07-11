package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"freegate/internal/config"
	"freegate/internal/server"
)

// Build-time metadata, injected via -ldflags by GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Load .env if present; silently skip if the file does not exist so that
	// production deployments driven purely by real env vars are unaffected.
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("warn: .env load: %v", err)
	}

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config:\n%s\n", err)
		os.Exit(1)
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatalf("create server: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
