// Package discord is the Discord side of Plumb: alert messages, the Ack
// button and slash commands.
//
// Security model: Plumb is read-only. The single write it can make is
// creating an Alertmanager silence when a member with the Platform role
// clicks Ack. Everything else only reads.
package discord

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

// API is the subset of *discordgo.Session Plumb uses (fakeable in tests).
type API interface {
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageEditComplex(m *discordgo.MessageEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
	InteractionRespond(interaction *discordgo.Interaction, resp *discordgo.InteractionResponse, options ...discordgo.RequestOption) error
	InteractionResponseEdit(interaction *discordgo.Interaction, newresp *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error)
}

// Silencer is the part of alertmanager.Client used by Ack.
type Silencer interface {
	CreateSilence(ctx context.Context, s alertmanager.Silence) (string, error)
}

// Options configure a Bot.
type Options struct {
	GuildID        string
	AlertChannelID string
	PlatformRoleID string
	PingOnCritical bool
	GrafanaURL     string
}

// Bot handles alerts and interactions.
type Bot struct {
	api API
	opt Options
	am  interface {
		Silencer
		AlertLister
	}
	q   Querier
	log *slog.Logger
	now func() time.Time

	mu   sync.Mutex
	msgs map[string]string // Alertmanager groupKey -> Discord message ID

	ready atomic.Bool
}

// New builds a Bot around an API (usually a *discordgo.Session).
func New(api API, opt Options, am interface {
	Silencer
	AlertLister
}, q Querier, log *slog.Logger) *Bot {
	return &Bot{api: api, opt: opt, am: am, q: q, log: log, now: time.Now, msgs: map[string]string{}}
}

// Commands are the slash commands registered in the guild.
var Commands = []*discordgo.ApplicationCommand{
	{Name: "status", Description: "Health summary of the Oak cluster (read-only)"},
}

// Attach wires handlers onto a live session and registers slash commands
// once the gateway is ready.
func (b *Bot) Attach(s *discordgo.Session) {
	s.Identify.Intents = discordgo.IntentsGuilds
	s.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		if _, err := s.ApplicationCommandBulkOverwrite(r.User.ID, b.opt.GuildID, Commands); err != nil {
			b.log.Error("registering slash commands failed", "err", err)
			return
		}
		b.ready.Store(true)
		b.log.Info("connected to Discord", "user", r.User.Username)
	})
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.Disconnect) { b.ready.Store(false) })
	s.AddHandler(func(s *discordgo.Session, _ *discordgo.Resumed) { b.ready.Store(true) })
	s.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) { b.HandleInteraction(i.Interaction) })
}

// Ready reports whether the gateway connection is up.
func (b *Bot) Ready() bool { return b.ready.Load() }

// HandleAlerts posts or updates the message for one Alertmanager group.
func (b *Bot) HandleAlerts(ctx context.Context, w alertmanager.Webhook) error {
	embed := AlertEmbed(w)
	components := AckButtons()
	if w.Status == "resolved" {
		components = []discordgo.MessageComponent{}
	}

	b.mu.Lock()
	msgID, known := b.msgs[w.GroupKey]
	b.mu.Unlock()

	if known {
		embeds := []*discordgo.MessageEmbed{embed}
		_, err := b.api.ChannelMessageEditComplex(&discordgo.MessageEdit{
			ID: msgID, Channel: b.opt.AlertChannelID, Embeds: &embeds, Components: &components,
		}, discordgo.WithContext(ctx))
		if err == nil {
			b.forgetIfResolved(w)
			return nil
		}
		// Message deleted by someone? Fall through and post a new one.
		b.log.Warn("editing alert message failed, posting a new one", "err", err)
	}

	send := &discordgo.MessageSend{Embeds: []*discordgo.MessageEmbed{embed}, Components: components,
		AllowedMentions: &discordgo.MessageAllowedMentions{}}
	if b.opt.PingOnCritical && w.Status == "firing" && severity(w) == "critical" {
		send.Content = "<@&" + b.opt.PlatformRoleID + ">"
		send.AllowedMentions.Roles = []string{b.opt.PlatformRoleID}
	}
	m, err := b.api.ChannelMessageSendComplex(b.opt.AlertChannelID, send, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("posting alert: %w", err)
	}
	b.mu.Lock()
	b.msgs[w.GroupKey] = m.ID
	b.mu.Unlock()
	b.forgetIfResolved(w)
	return nil
}

func (b *Bot) forgetIfResolved(w alertmanager.Webhook) {
	if w.Status == "resolved" {
		b.mu.Lock()
		delete(b.msgs, w.GroupKey)
		b.mu.Unlock()
	}
}

// allowed checks guild and Platform role.
func (b *Bot) allowed(i *discordgo.Interaction) bool {
	return i.GuildID == b.opt.GuildID && i.Member != nil && slices.Contains(i.Member.Roles, b.opt.PlatformRoleID)
}

func userOf(i *discordgo.Interaction) (id, name string) {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID, i.Member.User.Username
	}
	if i.User != nil {
		return i.User.ID, i.User.Username
	}
	return "", "unknown"
}

// HandleInteraction answers slash commands and button clicks.
func (b *Bot) HandleInteraction(i *discordgo.Interaction) {
	uid, uname := userOf(i)
	if !b.allowed(i) {
		b.log.Warn("denied interaction", "user", uname, "user_id", uid, "guild", i.GuildID)
		b.ephemeral(i, "You need the Platform role in the BitsAndBricks server to use Plumb.")
		return
	}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		cmd := i.ApplicationCommandData().Name
		b.log.Info("command", "audit", true, "command", cmd, "user", uname, "user_id", uid)
		switch cmd {
		case "status":
			b.status(i)
		default:
			b.ephemeral(i, "Unknown command.")
		}
	case discordgo.InteractionMessageComponent:
		b.ack(i, uid, uname)
	}
}

func (b *Bot) status(i *discordgo.Interaction) {
	if err := b.api.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
	}); err != nil {
		b.log.Error("deferring /status failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	embeds := []*discordgo.MessageEmbed{StatusEmbed(ctx, b.q, b.am, b.opt.GrafanaURL)}
	if _, err := b.api.InteractionResponseEdit(i, &discordgo.WebhookEdit{Embeds: &embeds}); err != nil {
		b.log.Error("answering /status failed", "err", err)
	}
}

func (b *Bot) ack(i *discordgo.Interaction, uid, uname string) {
	data := i.MessageComponentData()
	dur, ok := ackDurations[data.CustomID]
	if !ok || i.Message == nil || len(i.Message.Embeds) == 0 || i.Message.Embeds[0].Footer == nil {
		b.ephemeral(i, "That button isn't something Plumb understands.")
		return
	}
	labels, err := ParseGroupFooter(i.Message.Embeds[0].Footer.Text)
	if err != nil {
		b.ephemeral(i, "Couldn't read which alert this is: "+err.Error())
		return
	}
	matchers := make([]alertmanager.Matcher, 0, len(labels))
	for k, v := range labels {
		matchers = append(matchers, alertmanager.Matcher{Name: k, Value: v, IsEqual: true})
	}
	slices.SortFunc(matchers, func(a, b alertmanager.Matcher) int {
		if a.Name < b.Name {
			return -1
		}
		return 1
	})
	start := b.now()
	end := start.Add(dur)
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	id, err := b.am.CreateSilence(ctx, alertmanager.Silence{
		Matchers: matchers, StartsAt: start, EndsAt: end,
		CreatedBy: uname + " (Discord via Plumb)",
		Comment:   fmt.Sprintf("Acked in Discord by %s for %s", uname, dur),
	})
	if err != nil {
		b.log.Error("ack failed", "audit", true, "user", uname, "user_id", uid, "labels", labels, "err", err)
		b.ephemeral(i, "Ack failed, Alertmanager said: "+truncate(err.Error(), 300))
		return
	}
	b.log.Info("ack", "audit", true, "user", uname, "user_id", uid, "labels", labels, "until", end, "silence", id)

	embed := *i.Message.Embeds[0]
	embed.Fields = append(slices.Clone(embed.Fields), &discordgo.MessageEmbedField{
		Name:  "Acked",
		Value: fmt.Sprintf("by <@%s> until <t:%d:f> (silence `%s`)", uid, end.Unix(), id),
	})
	if err := b.api.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Embeds:     []*discordgo.MessageEmbed{&embed},
			Components: AckedButtons(uname),
		},
	}); err != nil {
		b.log.Error("updating acked message failed", "err", err)
	}
}

func (b *Bot) ephemeral(i *discordgo.Interaction, msg string) {
	if err := b.api.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: msg, Flags: discordgo.MessageFlagsEphemeral},
	}); err != nil {
		b.log.Error("ephemeral reply failed", "err", err)
	}
}
