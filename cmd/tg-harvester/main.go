// Command tg-harvester forwards new messages from Telegram channels to an
// HTTP endpoint and serves the htmx UI used to configure it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"tg-harvester/internal/adapter/forward"
	"tg-harvester/internal/adapter/sqlite"
	"tg-harvester/internal/adapter/telegram"
	"tg-harvester/internal/adapter/web"
	"tg-harvester/internal/app"
	"tg-harvester/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := cfg.Logger(os.Stderr)
	log.Info("starting tg-harvester", "config", cfg.String())

	db, err := sqlite.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close database", "err", err)
		}
	}()
	store := sqlite.New(db)

	tgClient := telegram.New(store, log)
	forwarder := forward.New(&http.Client{Timeout: cfg.HTTPTimeout})

	session := app.NewSessionManager(tgClient, store, log)
	harvester := app.NewHarvester(tgClient, forwarder, store, store, log)
	scheduler := app.NewScheduler(harvester, session, store, log)
	channels := app.NewChannelService(store, tgClient)
	settings := app.NewSettingsService(store, forwarder, scheduler.Refresh)
	status := app.NewStatusService(session, scheduler, store, store)

	handler, err := web.New(web.Deps{
		Account:  session,
		Channels: channels,
		Settings: settings,
		Status:   status,
		Harvest:  scheduler,
		Log:      log,
	}, cfg.BasicAuth)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Photo-heavy payloads are written by the forwarder, not read here,
		// so a generous but bounded write timeout is enough.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  2 * time.Minute,
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		if err := session.Run(gctx); err != nil {
			return fmt.Errorf("telegram session: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := scheduler.Run(gctx); err != nil {
			return fmt.Errorf("scheduler: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		log.Info("web ui listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-gctx.Done()
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(gctx), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})
	return g.Wait()
}
