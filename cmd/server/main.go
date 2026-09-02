// Command server is the entry point for the BeeBase media-service.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/config"
	"github.com/sbezhuk/beebase-media-service/internal/platform/postgres"
	repopostgres "github.com/sbezhuk/beebase-media-service/internal/repository/postgres"
	transporthttp "github.com/sbezhuk/beebase-media-service/internal/transport/http"
	mediahttp "github.com/sbezhuk/beebase-media-service/internal/transport/http/media"

	"github.com/sbezhuk/beebase-common/authmw"
	"github.com/sbezhuk/beebase-common/logger"
	"github.com/sbezhuk/beebase-common/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// .env is optional: present in local dev, absent in production/containers.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.Env, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	connectCtx, cancelConnect := context.WithTimeout(ctx, cfg.DatabaseConnectTimeout)
	db, err := postgres.New(connectCtx, cfg.DatabaseURL)
	cancelConnect()
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()

	log.Info("connected to database")

	// Fails fast at boot if auth-service's JWKS endpoint isn't reachable,
	// consistent with how the database connection above is handled;
	// docker-compose orders auth-service before this service accordingly.
	verifier, err := authmw.NewVerifierFromJWKSURL(ctx, cfg.AuthJWKSURL)
	if err != nil {
		return fmt.Errorf("build JWKS verifier: %w", err)
	}

	mediaRepo := repopostgres.NewMediaRepository(db)
	mediaService := appmedia.NewService(mediaRepo, cfg.MaxUploadSizeBytes)
	mediaHandler := mediahttp.NewHandler(mediaService, log, cfg.MaxUploadSizeBytes)

	router := transporthttp.NewRouter(log, db, mediaHandler, verifier)

	srv := server.New(server.Config{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      router,
		ReadTimeout:  cfg.HTTPReadTimeout,
		WriteTimeout: cfg.HTTPWriteTimeout,
		IdleTimeout:  cfg.HTTPIdleTimeout,
	})

	errCh := make(chan error, 1)
	go func() {
		log.Info("starting http server", "port", cfg.HTTPPort, "env", cfg.Env)
		errCh <- srv.Run()
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("run server: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("server stopped cleanly")
	return nil
}
