// Command llmobsd is the LLMObs kernel daemon (lite profile). It runs OTLP
// ingestion, the middleware chain, storage, and the Query API in one process.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/m7mdhka/llmobs/kernel/internal/controlplane"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/ingest"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/normalize"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/pipeline"
	"github.com/m7mdhka/llmobs/kernel/internal/dataplane/query"
	"github.com/m7mdhka/llmobs/kernel/internal/platform"
	"github.com/m7mdhka/llmobs/kernel/internal/storage/postgres"
	"github.com/m7mdhka/llmobs/kernel/pkg/brand"
)

func main() {
	if err := run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func run() error {
	cfg, err := platform.LoadConfig()
	if err != nil {
		return err
	}
	log := platform.NewLogger(cfg)
	log.Info(brand.Name+" kernel starting", "otlp_http", cfg.OTLPHTTPAddr, "api", cfg.APIAddr)

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := platform.NewPGPool(rootCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// wait for the database to be reachable (compose start ordering)
	if err := waitForDB(rootCtx, pool, log); err != nil {
		return err
	}

	if cfg.MigrateOnBoot {
		if err := postgres.Migrate(rootCtx, pool); err != nil {
			return err
		}
		log.Info("migrations applied")
	}

	project, key, created, err := controlplane.Bootstrap(rootCtx, pool, cfg.BootstrapProject, cfg.BootstrapAPIKey)
	if err != nil {
		return err
	}
	if created {
		// print the key exactly once
		log.Warn("bootstrap created a default api key — store it now, it is not shown again",
			"project", project, "api_key", key)
	}

	store := postgres.NewStore(pool)
	reg := normalize.Default()
	pipe := pipeline.New(pool, store, reg, pipeline.NoopBus{}, pipeline.Config{})

	receiver := ingest.NewReceiver(pipe, log, 4096, 4)
	receiver.Start(rootCtx)

	maxWindow, _ := time.ParseDuration(cfg.QueryMaxWindow)
	qsrv := query.NewServer(store, pool, log, maxWindow)

	// API server: query + health.
	apiMux := http.NewServeMux()
	platform.NewHealth(pool).Register(apiMux)
	apiMux.Handle("/v1alpha1/", qsrv.Handler())
	apiServer := &http.Server{Addr: cfg.APIAddr, Handler: apiMux, ReadHeaderTimeout: 5 * time.Second}

	// OTLP HTTP receiver server.
	otlpServer := &http.Server{Addr: cfg.OTLPHTTPAddr, Handler: receiver.Handler(), ReadHeaderTimeout: 5 * time.Second}

	// OTLP gRPC receiver server (4317).
	grpcServer := grpc.NewServer()
	receiver.RegisterGRPC(grpcServer)
	grpcLis, err := net.Listen("tcp", cfg.OTLPGRPCAddr)
	if err != nil {
		return err
	}

	errCh := make(chan error, 3)
	go serve(apiServer, log, "api", errCh)
	go serve(otlpServer, log, "otlp-http", errCh)
	go func() {
		log.Info("otlp-grpc listening", "addr", cfg.OTLPGRPCAddr)
		if err := grpcServer.Serve(grpcLis); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		log.Error("server error", "err", err.Error())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = apiServer.Shutdown(shutdownCtx)
	_ = otlpServer.Shutdown(shutdownCtx)
	grpcServer.GracefulStop()
	receiver.Stop()
	log.Info("stopped")
	return nil
}

func serve(s *http.Server, log *slog.Logger, name string, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
	}
	_ = log
	_ = name
}

func waitForDB(ctx context.Context, pool interface {
	Ping(context.Context) error
}, log *slog.Logger) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := pool.Ping(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("database not reachable within 60s")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
			log.Info("waiting for database")
		}
	}
}
