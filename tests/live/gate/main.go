// Command gate is a developer-only, persistent-budget Responses proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "live gate failed:", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("ledger", "artifacts/live-budget.json", "existing persistent USD10 development ledger")
	flag.Parse()
	if os.Getenv("QUAL_RUN_LLM") != "1" {
		return errors.New("QUAL_RUN_LLM=1 required")
	}
	key, token := os.Getenv("QUAL_LIVE_UPSTREAM_KEY"), os.Getenv("QUAL_LIVE_GATE_TOKEN")
	if key == "" || len(token) < 32 || key == token {
		return errors.New("distinct upstream key and random gate token (at least 32 characters) required in environment")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := updateLedger(ctx, *path, func(*ledger) error { return nil }); err != nil {
		return err
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on localhost: %w", err)
	}
	client := &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: newGate(*path, token, key, "https://api.openai.com", client), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 4 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	return serve(ctx, server, listener)
}

func serve(ctx context.Context, server *http.Server, listener net.Listener) error {
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	fmt.Printf("ready http://%s/v1\n", listener.Addr())
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
