package discord

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/statuspage"
)

// /notice posts a status page notice as Plumb's own message in the notice
// channel. Discord is the store: Plumb reads its own messages back, so it
// keeps no state on the cluster and still changes nothing there.

const noticeFooter = "plumb notice"

var noticeCommand = &discordgo.ApplicationCommand{
	Name:        "notice",
	Description: "Post a notice on the public status page",
	Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionString, Name: "kind", Description: "What kind of notice", Required: true,
			Choices: []*discordgo.ApplicationCommandOptionChoice{
				{Name: "Incident (something is broken)", Value: "incident"},
				{Name: "Maintenance (planned work)", Value: "maintenance"},
				{Name: "Info", Value: "info"},
			}},
		{Type: discordgo.ApplicationCommandOptionString, Name: "title", Description: "Short title, shown publicly", Required: true, MaxLength: 100},
		{Type: discordgo.ApplicationCommandOptionString, Name: "details", Description: "A sentence or two, shown publicly", MaxLength: 1000},
		{Type: discordgo.ApplicationCommandOptionString, Name: "services", Description: "Affected services, comma separated, as named on the page"},
		{Type: discordgo.ApplicationCommandOptionString, Name: "start", Description: `"now" (default) or a time like 2026-11-20 20:00 (Zurich time)`},
		{Type: discordgo.ApplicationCommandOptionString, Name: "duration", Description: "How long, like 30m or 2h. Leave empty for incidents until resolved."},
	},
}

var discordTime = regexp.MustCompile(`<t:(\d+):[a-zA-Z]>`)

// parseStart understands "now", "YYYY-MM-DD HH:MM" and "HH:MM" (today).
func parseStart(s string, now time.Time, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "now") {
		return now, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		n := now.In(loc)
		return time.Date(n.Year(), n.Month(), n.Day(), t.Hour(), t.Minute(), 0, 0, loc), nil
	}
	return time.Time{}, fmt.Errorf("start %q: use now, 20:00 or 2026-11-20 20:00", s)
}

// NoticeEmbed renders a notice. NoticeFromEmbed reads it back.
func NoticeEmbed(n statuspage.Notice) *discordgo.MessageEmbed {
	color := map[string]int{"incident": colorCritical, "maintenance": 0x60A5FA, "info": 0x8C8C8C}[n.Kind]
	if !n.End.IsZero() && !n.End.After(time.Now()) {
		color = colorResolved
	}
	e := &discordgo.MessageEmbed{
		Title:       truncate(n.Title, 256),
		Description: n.Body,
		Color:       color,
		Footer:      &discordgo.MessageEmbedFooter{Text: noticeFooter},
		Fields: []*discordgo.MessageEmbedField{
			{Name: "Kind", Value: n.Kind, Inline: true},
			{Name: "Start", Value: fmt.Sprintf("<t:%d:f>", n.Start.Unix()), Inline: true},
		},
	}
	if !n.End.IsZero() {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "End", Value: fmt.Sprintf("<t:%d:f>", n.End.Unix()), Inline: true})
	}
	if len(n.Services) > 0 {
		e.Fields = append(e.Fields, &discordgo.MessageEmbedField{Name: "Services", Value: strings.Join(n.Services, ", ")})
	}
	return e
}

func NoticeFromEmbed(e *discordgo.MessageEmbed) (statuspage.Notice, bool) {
	if e == nil || e.Footer == nil || e.Footer.Text != noticeFooter {
		return statuspage.Notice{}, false
	}
	n := statuspage.Notice{Title: e.Title, Body: e.Description}
	for _, f := range e.Fields {
		switch f.Name {
		case "Kind":
			n.Kind = f.Value
		case "Start", "End":
			m := discordTime.FindStringSubmatch(f.Value)
			if m == nil {
				return n, false
			}
			sec, _ := strconv.ParseInt(m[1], 10, 64)
			if f.Name == "Start" {
				n.Start = time.Unix(sec, 0)
			} else {
				n.End = time.Unix(sec, 0)
			}
		case "Services":
			for _, s := range strings.Split(f.Value, ",") {
				if s = strings.TrimSpace(s); s != "" {
					n.Services = append(n.Services, s)
				}
			}
		}
	}
	if n.Kind == "" || n.Start.IsZero() {
		return n, false
	}
	return n, true
}

func resolveButton() []discordgo.MessageComponent {
	return []discordgo.MessageComponent{discordgo.ActionsRow{Components: []discordgo.MessageComponent{
		discordgo.Button{Label: "Resolve", Style: discordgo.SuccessButton, CustomID: "plumb:notice:resolve"},
	}}}
}

func (b *Bot) noticeChannel() string {
	if b.opt.NoticeChannelID != "" {
		return b.opt.NoticeChannelID
	}
	return b.opt.AlertChannelID
}

// postNotice handles /notice.
func (b *Bot) postNotice(i *discordgo.Interaction, uid, uname string) {
	opts := map[string]string{}
	for _, o := range i.ApplicationCommandData().Options {
		opts[o.Name] = o.StringValue()
	}
	now := b.now()
	start, err := parseStart(opts["start"], now, b.loc())
	if err != nil {
		b.ephemeral(i, err.Error())
		return
	}
	n := statuspage.Notice{Title: strings.TrimSpace(opts["title"]), Kind: opts["kind"], Body: strings.TrimSpace(opts["details"]), Start: start}
	if d := strings.TrimSpace(opts["duration"]); d != "" {
		dur, err := time.ParseDuration(d)
		if err != nil || dur <= 0 || dur > 14*24*time.Hour {
			b.ephemeral(i, fmt.Sprintf("duration %q: use something like 30m, 2h or 1h30m (max 14 days)", d))
			return
		}
		n.End = start.Add(dur)
	} else if n.Kind == "maintenance" {
		b.ephemeral(i, "Maintenance needs a duration, so the page knows when it ends.")
		return
	}
	for _, s := range strings.Split(opts["services"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			n.Services = append(n.Services, s)
		}
	}
	if n.Title == "" {
		b.ephemeral(i, "The notice needs a title.")
		return
	}
	embed := NoticeEmbed(n)
	embed.Author = &discordgo.MessageEmbedAuthor{Name: "Posted by " + uname}
	if _, err := b.api.ChannelMessageSendComplex(b.noticeChannel(), &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed}, Components: resolveButton(),
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	}); err != nil {
		b.ephemeral(i, "Posting the notice failed: "+truncate(err.Error(), 300))
		return
	}
	b.log.Info("notice", "audit", true, "user", uname, "user_id", uid, "kind", n.Kind, "title", n.Title, "start", n.Start, "end", n.End)
	b.ephemeral(i, "Posted. It shows on the status page within a few seconds. Use its Resolve button when it's over.")
	b.noticesChanged()
}

// resolveNotice handles the Resolve button: the notice ends now.
func (b *Bot) resolveNotice(i *discordgo.Interaction, uid, uname string) {
	if i.Message == nil || len(i.Message.Embeds) == 0 {
		b.ephemeral(i, "That isn't a notice.")
		return
	}
	n, ok := NoticeFromEmbed(i.Message.Embeds[0])
	if !ok {
		b.ephemeral(i, "That isn't a notice.")
		return
	}
	now := b.now()
	if n.End.IsZero() || n.End.After(now) {
		n.End = now
	}
	if n.Start.After(n.End) {
		n.Start = n.End // a future maintenance that was called off
	}
	embed := NoticeEmbed(n)
	embed.Author = i.Message.Embeds[0].Author
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{Name: "Resolved by", Value: uname, Inline: true})
	if err := b.api.InteractionRespond(i, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}, Components: []discordgo.MessageComponent{}},
	}); err != nil {
		b.log.Error("resolving notice failed", "err", err)
		return
	}
	b.log.Info("notice resolved", "audit", true, "user", uname, "user_id", uid, "title", n.Title)
	b.noticesChanged()
}

// Notices reads the notices back from Plumb's own messages (last 100).
func (b *Bot) Notices(ctx context.Context) ([]statuspage.Notice, error) {
	b.mu.Lock()
	self := b.selfID
	b.mu.Unlock()
	if self == "" {
		// Without our own ID any author's embed would pass as a notice.
		return nil, errors.New("not connected to Discord yet")
	}
	msgs, err := b.api.ChannelMessages(b.noticeChannel(), 100, "", "", "", discordgo.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	var out []statuspage.Notice
	for _, m := range msgs {
		if m.Author == nil || m.Author.ID != self || len(m.Embeds) == 0 {
			continue
		}
		if n, ok := NoticeFromEmbed(m.Embeds[0]); ok {
			out = append(out, n)
		}
	}
	return out, nil
}

func (b *Bot) noticesChanged() {
	if b.OnNotice != nil {
		go b.OnNotice()
	}
}
