package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	embed "github.com/hatchet-dev/hatchet-embedded"
)

type handshake struct {
	Token       string `json:"token"`
	TenantID    string `json:"tenant_id"`
	GRPCAddress string `json:"grpc_address"`
	APIURL      string `json:"api_url"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sidecar:", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := flag.String("database-url", "", "use an existing Postgres instead of the bundled one")
	rabbitMQURL := flag.String("rabbitmq-url", "", "use RabbitMQ instead of the Postgres message queue")
	grpcPort := flag.Int("grpc-port", 0, "bind the gRPC server to this port")
	apiPort := flag.Int("api-port", 0, "bind the REST API to this port")
	noAPI := flag.Bool("no-api", false, "start only the engine + gRPC, no REST API")
	noMigrations := flag.Bool("no-migrations", false, "skip running migrations on startup")
	logLevel := flag.String("log-level", "", "engine log level")
	handshakeFile := flag.String("handshake-file", "", "write connection info as JSON to this file once ready")
	flag.Parse()

	if *handshakeFile == "" {
		return fmt.Errorf("-handshake-file is required")
	}

	var opts []embed.Option
	if *databaseURL != "" {
		opts = append(opts, embed.WithPostgres(*databaseURL))
	}
	if *rabbitMQURL != "" {
		opts = append(opts, embed.WithRabbitMQ(*rabbitMQURL))
	}
	if *grpcPort != 0 {
		opts = append(opts, embed.WithGRPCPort(*grpcPort))
	}
	if *apiPort != 0 {
		opts = append(opts, embed.WithAPIPort(*apiPort))
	}
	if *noAPI {
		opts = append(opts, embed.WithoutAPI())
	}
	if *noMigrations {
		opts = append(opts, embed.WithoutMigrations())
	}
	if *logLevel != "" {
		opts = append(opts, embed.WithLogLevel(*logLevel))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	inst, err := embed.StartServer(ctx, opts...)
	if err != nil {
		return err
	}

	if err := writeHandshake(*handshakeFile, handshake{
		Token:       inst.Token(),
		TenantID:    inst.TenantID(),
		GRPCAddress: inst.GRPCAddress(),
		APIURL:      inst.APIURL(),
	}); err != nil {
		_ = inst.Shutdown(context.Background())
		return err
	}

	stdinClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdinClosed)
	}()

	select {
	case <-ctx.Done():
	case <-stdinClosed:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return inst.Shutdown(shutdownCtx)
}

func writeHandshake(path string, h handshake) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
