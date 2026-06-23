// Command sluice-server runs the Sluice HTTP API.
//
// Usage:
//
//	sluice-server [--addr :8080] [--config config.json] [--data ./testdata]
//
// With --config it loads API keys and quotas from a JSON file; without it, a
// dev default is used (data dir from --data, a single "dev-key" API key).
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PG1204/sluice/api"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	configPath := flag.String("config", "", "path to JSON config (optional; uses a dev default if empty)")
	dataDir := flag.String("data", "./testdata", "data directory (used only when --config is not given)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig(*configPath, *dataDir)
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.NewServer(cfg, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGINT/SIGTERM.
	go func() {
		log.Info("listening", "addr", *addr, "data_dir", cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown", "err", err)
	}
	log.Info("stopped")
}

// loadConfig loads the config file if given, otherwise builds a dev default.
func loadConfig(path, dataDir string) (api.Config, error) {
	if path == "" {
		return api.DefaultConfig(dataDir), nil
	}
	return api.LoadConfig(path)
}
