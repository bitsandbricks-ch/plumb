package discord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func command(roles []string, name string, opts map[string]string) *discordgo.Interaction {
	var o []*discordgo.ApplicationCommandInteractionDataOption
	for k, v := range opts {
		o = append(o, &discordgo.ApplicationCommandInteractionDataOption{Name: k, Type: discordgo.ApplicationCommandOptionString, Value: v})
	}
	return &discordgo.Interaction{
		Type: discordgo.InteractionApplicationCommand, GuildID: "g",
		Member: &discordgo.Member{Roles: roles, User: &discordgo.User{ID: "u1", Username: "mike"}},
		Data:   discordgo.ApplicationCommandInteractionData{Name: name, Options: o},
	}
}

func TestNoticeRoundTrip(t *testing.T) {
	b, api, _ := newBot()
	zurich, _ := time.LoadLocation("Europe/Zurich")
	b.opt.Location = zurich
	b.now = func() time.Time { return time.Date(2026, 11, 16, 15, 0, 0, 0, zurich) }
	called := make(chan struct{}, 1)
	b.OnNotice = func() { called <- struct{}{} }

	b.HandleInteraction(command([]string{"r"}, "notice", map[string]string{
		"kind": "maintenance", "title": "Postgres upgrade", "details": "Short downtime.",
		"services": "Seatbit, Floorbit", "start": "2026-11-20 20:00", "duration": "1h",
	}))
	if len(api.sent) != 1 {
		t.Fatalf("expected the notice to be posted, responses: %+v", api.responses)
	}
	<-called
	e := api.sent[0].Embeds[0]
	n, ok := NoticeFromEmbed(e)
	if !ok || n.Title != "Postgres upgrade" || n.Kind != "maintenance" || len(n.Services) != 2 || n.End.Sub(n.Start) != time.Hour {
		t.Fatalf("round trip %+v ok=%v", n, ok)
	}
	if n.Start.In(zurich).Hour() != 20 {
		t.Fatalf("start %v", n.Start)
	}

	// Read back: only the bot's own messages count.
	b.selfID = "bot"
	api.history = []*discordgo.Message{
		{Author: &discordgo.User{ID: "bot"}, Embeds: []*discordgo.MessageEmbed{e}},
		{Author: &discordgo.User{ID: "someone"}, Embeds: []*discordgo.MessageEmbed{e}},
		{Author: &discordgo.User{ID: "bot"}, Embeds: []*discordgo.MessageEmbed{{Title: "an alert", Footer: &discordgo.MessageEmbedFooter{Text: "group: x"}}}},
	}
	ns, err := b.Notices(context.Background())
	if err != nil || len(ns) != 1 {
		t.Fatalf("%+v %v", ns, err)
	}

	// Resolve ends it now.
	i := click([]string{"r"}, "plumb:notice:resolve", "")
	i.Message.Embeds = []*discordgo.MessageEmbed{e}
	b.HandleInteraction(i)
	<-called
	r := api.responses[len(api.responses)-1]
	got, _ := NoticeFromEmbed(r.Data.Embeds[0])
	if r.Type != discordgo.InteractionResponseUpdateMessage || !got.End.Equal(b.now()) || len(r.Data.Components) != 0 {
		t.Fatalf("resolve %+v", got)
	}
}

func TestNoticeValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"maintenance without duration": {"kind": "maintenance", "title": "x"},
		"bad start":                    {"kind": "incident", "title": "x", "start": "tomorrow-ish"},
		"bad duration":                 {"kind": "incident", "title": "x", "duration": "forever"},
	}
	for name, opts := range cases {
		b, api, _ := newBot()
		b.HandleInteraction(command([]string{"r"}, "notice", opts))
		if len(api.sent) != 0 || len(api.responses) != 1 || api.responses[0].Data.Flags != discordgo.MessageFlagsEphemeral {
			t.Errorf("%s: should be refused privately", name)
		}
	}
	b, api, _ := newBot()
	b.HandleInteraction(command([]string{"other"}, "notice", map[string]string{"kind": "incident", "title": "x"}))
	if len(api.sent) != 0 || !strings.Contains(api.responses[0].Data.Content, "Platform role") {
		t.Fatal("notice without the role must be refused")
	}
}
