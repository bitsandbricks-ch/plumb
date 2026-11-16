// Plumb is the BitsAndBricks Platform bot: it posts Oak alerts to Discord
// with an Ack button, answers read-only slash commands, and serves the
// public status page.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
	"github.com/bitsandbricks-ch/plumb/internal/config"
	"github.com/bitsandbricks-ch/plumb/internal/discord"
	"github.com/bitsandbricks-ch/plumb/internal/prom"
	"github.com/bitsandbricks-ch/plumb/internal/statuspage"
	"github.com/bitsandbricks-ch/plumb/internal/web"
)

var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("plumb stopped", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return err
	}
	am := alertmanager.NewClient(cfg.AlertmanagerURL)
	bot := discord.New(session, discord.Options{
		GuildID: cfg.GuildID, AlertChannelID: cfg.AlertChannelID, PlatformRoleID: cfg.PlatformRoleID,
		PingOnCritical: cfg.PingOnCritical, GrafanaURL: cfg.GrafanaURL,
	}, am, prom.NewClient(cfg.MetricsURL), log)
	bot.Attach(session)
	if err := session.Open(); err != nil {
		return err
	}
	defer session.Close()

	internal := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           web.Handler(cfg.WebhookToken, bot.HandleAlerts, bot.Ready, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	servers := []*http.Server{internal}

	if len(cfg.StatusServices) > 0 {
		checker := statuspage.NewChecker(cfg.StatusTitle, cfg.StatusServices, cfg.StatusSlow, log)
		go checker.Run(ctx, cfg.StatusInterval)
		servers = append(servers, &http.Server{
			Addr: cfg.StatusAddr, Handler: checker.Handler(),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		})
	} else {
		log.Info("PLUMB_STATUS_SERVICES is empty, status page disabled")
	}

	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	log.Info("plumb started", "version", version, "listen", cfg.ListenAddr, "status", cfg.StatusAddr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdown)
	}
	return nil
}
