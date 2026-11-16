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
	_ "time/tzdata" // the image has no zoneinfo

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
	loc, _ := time.LoadLocation(cfg.TimeZone)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	session, err := discordgo.New("Bot " + cfg.DiscordToken)
	if err != nil {
		return err
	}
	am := alertmanager.NewClient(cfg.AlertmanagerURL)
	metrics := prom.NewClient(cfg.MetricsURL)
	bot := discord.New(session, discord.Options{
		GuildID: cfg.GuildID, AlertChannelID: cfg.AlertChannelID, PlatformRoleID: cfg.PlatformRoleID,
		PingOnCritical: cfg.PingOnCritical, GrafanaURL: cfg.GrafanaURL,
		NoticeChannelID: cfg.NoticeChannel, Location: loc,
	}, am, metrics, log)
	bot.Attach(session)
	if err := session.Open(); err != nil {
		return err
	}
	defer session.Close()

	var checker *statuspage.Checker
	if len(cfg.StatusServices) > 0 || cfg.Discovery {
		var static []statuspage.Target
		for i, s := range cfg.StatusServices {
			// Order as listed; discovered apps (order 100 by default) follow.
			static = append(static, statuspage.Target{Name: s.Name, URL: s.URL, Group: "Apps", Order: i})
		}
		opt := statuspage.Options{
			Title: cfg.StatusTitle, Host: cfg.StatusHost, Slow: cfg.StatusSlow, Location: loc,
			NoticesFile: cfg.NoticesFile, NoticeFeed: bot.Notices, History: metrics, Log: log,
		}
		if cfg.Discovery {
			d, err := statuspage.InClusterDiscoverer()
			if err != nil {
				return err
			}
			opt.Discover = d.Discover
		}
		checker = statuspage.NewChecker(static, opt)
		cluster := statuspage.Cluster{Q: metrics, AM: am}
		checker.AddComponent(statuspage.Component{Name: "Kubernetes cluster", Check: cluster.Check})
		checker.AddComponent(statuspage.Component{Name: "Discord bot", Check: func(context.Context) statuspage.State {
			if bot.Ready() {
				return statuspage.Up
			}
			return statuspage.Down
		}})
		checker.SetStats(cluster.Stats)
		bot.OnNotice = func() {
			// Discord needs a moment before the new message shows in history.
			time.Sleep(2 * time.Second)
			checker.RefreshNotices(ctx)
		}
		go checker.Run(ctx, cfg.StatusInterval)
	} else {
		log.Info("no status services configured, status page disabled")
	}

	var metricsHandler http.Handler
	if checker != nil {
		metricsHandler = checker.MetricsHandler()
	}
	servers := []*http.Server{{
		Addr:              cfg.ListenAddr,
		Handler:           web.Handler(cfg.WebhookToken, bot.HandleAlerts, bot.Ready, metricsHandler, log),
		ReadHeaderTimeout: 5 * time.Second,
	}}
	if checker != nil {
		servers = append(servers, &http.Server{
			Addr: cfg.StatusAddr, Handler: checker.Handler(),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		})
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
