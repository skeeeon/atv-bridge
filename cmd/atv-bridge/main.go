// Command atv-bridge controls Google TV and Android TV devices over NATS.
//
//	atv-bridge [-config path]           run the bridge
//	atv-bridge discover [-timeout 5s]   list Android TVs on the LAN
//
// Settings come from defaults, then the config file, then environment
// variables. TVs are listed in the devices KV bucket. See docs/atv-bridge.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stone-age-io/atv-bridge/bridge"
	"github.com/stone-age-io/atv-bridge/config"
	"github.com/stone-age-io/atv-bridge/internal/health"
	"github.com/stone-age-io/atv-bridge/internal/natsconn"
	"github.com/stone-age-io/atv-bridge/third_party/atvremote/remote"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "atv-bridge")
	var err error
	if len(os.Args) > 1 && os.Args[1] == "discover" {
		err = discover(os.Args[2:])
	} else {
		err = run(log, os.Args[1:])
	}
	if err != nil {
		log.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// discover prints the Android TVs that answer mDNS, as JSON lines. It needs
// to run on the TVs' LAN, and in a container only with host networking.
func discover(args []string) error {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	timeout := fs.Duration("timeout", 5*time.Second, "how long to listen for answers")
	_ = fs.Parse(args)
	devices, err := remote.Discover(context.Background(), *timeout)
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		fmt.Fprintln(os.Stderr, "no Android TVs answered; is this machine on their LAN, with host networking?")
	}
	enc := json.NewEncoder(os.Stdout)
	for _, d := range devices {
		_ = enc.Encode(d)
	}
	return nil
}

func run(log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("atv-bridge", flag.ExitOnError)
	path := fs.String("config", os.Getenv("CONFIG_FILE"), "config file (default "+config.DefaultPath+" if it exists)")
	_ = fs.Parse(args)

	cfg, err := config.Load(*path, os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	b := bridge.New(bridge.Options{
		BridgeID:          cfg.BridgeID,
		ClientName:        cfg.ClientName,
		SessionDir:        cfg.SessionDir,
		DevicesBucket:     cfg.DevicesBucket,
		StateBucket:       cfg.StateBucket,
		JSDomain:          cfg.NATS.JSDomain,
		CommandTimeout:    cfg.CommandTimeout,
		PairTimeout:       cfg.PairTimeout,
		HeartbeatInterval: cfg.HeartbeatInterval,
	}, log, bridge.NewMetrics(prometheus.DefaultRegisterer))

	// HTTP comes up first, so /api/ready answers "not ready" during startup.
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           health.Handler(version, time.Now(), b.Ready, prometheus.DefaultGatherer),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "error", err)
			os.Exit(1)
		}
	}()

	nc, err := natsconn.Connect(cfg.NATS.URL, cfg.NATS.Creds, "atv-bridge-"+cfg.BridgeID, log, natsconn.Hooks{
		Reconnected: b.RewriteAll,
	})
	if err != nil {
		return fmt.Errorf("connect %s: %w", cfg.NATS.URL, err)
	}

	startCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = b.Start(startCtx, nc)
	cancel()
	if err != nil {
		nc.Close()
		return err
	}
	svc, err := b.AddService(version)
	if err != nil {
		b.Stop()
		nc.Close()
		return fmt.Errorf("micro service: %w", err)
	}
	log.Info("running", "version", version, "bridge", cfg.BridgeID, "http", cfg.HTTPAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Info("shutting down")

	// Stop taking requests, let in-flight replies go out, then close the TVs.
	_ = svc.Stop()
	_ = nc.Drain()
	b.Stop()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}
