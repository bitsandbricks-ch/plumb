package discord

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/bitsandbricks-ch/plumb/internal/alertmanager"
)

type fakeAPI struct {
	history   []*discordgo.Message
	sent      []*discordgo.MessageSend
	edits     []*discordgo.MessageEdit
	responses []*discordgo.InteractionResponse
}

func (f *fakeAPI) ChannelMessageSendComplex(_ string, d *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.sent = append(f.sent, d)
	return &discordgo.Message{ID: "m1"}, nil
}
func (f *fakeAPI) ChannelMessages(string, int, string, string, string, ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	return f.history, nil
}
func (f *fakeAPI) ChannelMessageEditComplex(m *discordgo.MessageEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.edits = append(f.edits, m)
	return &discordgo.Message{ID: m.ID}, nil
}
func (f *fakeAPI) InteractionRespond(_ *discordgo.Interaction, r *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	f.responses = append(f.responses, r)
	return nil
}
func (f *fakeAPI) InteractionResponseEdit(*discordgo.Interaction, *discordgo.WebhookEdit, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{}, nil
}

type fakeSilencer struct {
	fakeAM
	got []alertmanager.Silence
}

func (f *fakeSilencer) CreateSilence(_ context.Context, s alertmanager.Silence) (string, error) {
	f.got = append(f.got, s)
	return "sil-1", nil
}

func newBot() (*Bot, *fakeAPI, *fakeSilencer) {
	api, am := &fakeAPI{}, &fakeSilencer{}
	b := New(api, Options{GuildID: "g", AlertChannelID: "c", PlatformRoleID: "r", PingOnCritical: true}, am, healthy(),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.now = func() time.Time { return time.Unix(1700000000, 0) }
	return b, api, am
}

func TestAlertLifecycle(t *testing.T) {
	b, api, _ := newBot()
	ctx := context.Background()
	if err := b.HandleAlerts(ctx, webhook("firing", 1)); err != nil {
		t.Fatal(err)
	}
	if len(api.sent) != 1 || api.sent[0].Content != "<@&r>" || len(api.sent[0].Components) != 1 {
		t.Fatalf("first post wrong: %+v", api.sent)
	}
	// Same group again (e.g. more pods): edits, doesn't re-post or re-ping.
	if err := b.HandleAlerts(ctx, webhook("firing", 2)); err != nil {
		t.Fatal(err)
	}
	if len(api.sent) != 1 || len(api.edits) != 1 || api.edits[0].ID != "m1" {
		t.Fatalf("expected edit: sent=%d edits=%d", len(api.sent), len(api.edits))
	}
	// Resolved: edit, buttons removed, group forgotten.
	if err := b.HandleAlerts(ctx, webhook("resolved", 2)); err != nil {
		t.Fatal(err)
	}
	if len(api.edits) != 2 || len(*api.edits[1].Components) != 0 {
		t.Fatalf("resolved edit wrong")
	}
	if len(b.msgs) != 0 {
		t.Fatal("resolved group should be forgotten")
	}
}

func click(roles []string, customID string, footer string) *discordgo.Interaction {
	return &discordgo.Interaction{
		Type:    discordgo.InteractionMessageComponent,
		GuildID: "g",
		Member:  &discordgo.Member{Roles: roles, User: &discordgo.User{ID: "u1", Username: "mike"}},
		Data:    discordgo.MessageComponentInteractionData{CustomID: customID},
		Message: &discordgo.Message{Embeds: []*discordgo.MessageEmbed{{Title: "Firing · X", Footer: &discordgo.MessageEmbedFooter{Text: footer}}}},
	}
}

func TestAckCreatesScopedSilence(t *testing.T) {
	b, api, am := newBot()
	b.HandleInteraction(click([]string{"r"}, "plumb:ack:4h", `group: alertname="PodCrashLooping" namespace="floorbit"`))
	if len(am.got) != 1 {
		t.Fatalf("expected one silence, got %d", len(am.got))
	}
	s := am.got[0]
	if len(s.Matchers) != 2 || s.Matchers[0].Name != "alertname" || s.Matchers[1].Value != "floorbit" || !s.Matchers[0].IsEqual || s.Matchers[0].IsRegex {
		t.Fatalf("matchers %+v", s.Matchers)
	}
	if s.EndsAt.Sub(s.StartsAt) != 4*time.Hour || !strings.HasPrefix(s.CreatedBy, "mike") {
		t.Fatalf("silence %+v", s)
	}
	r := api.responses[0]
	if r.Type != discordgo.InteractionResponseUpdateMessage || !strings.Contains(r.Data.Embeds[0].Fields[0].Value, "sil-1") {
		t.Fatalf("response %+v", r)
	}
}

func TestAckDenied(t *testing.T) {
	cases := map[string]*discordgo.Interaction{
		"no role": click([]string{"other"}, "plumb:ack:1h", `group: alertname="X"`),
		"wrong guild": func() *discordgo.Interaction {
			i := click([]string{"r"}, "plumb:ack:1h", `group: alertname="X"`)
			i.GuildID = "evil"
			return i
		}(),
		"dm": func() *discordgo.Interaction {
			i := click(nil, "plumb:ack:1h", `group: alertname="X"`)
			i.Member = nil
			return i
		}(),
		"bad button":  click([]string{"r"}, "plumb:ack:999h", `group: alertname="X"`),
		"empty group": click([]string{"r"}, "plumb:ack:1h", `group: `),
		"not alert":   click([]string{"r"}, "plumb:ack:1h", `hello`),
	}
	for name, i := range cases {
		b, api, am := newBot()
		b.HandleInteraction(i)
		if len(am.got) != 0 {
			t.Errorf("%s: silence created", name)
		}
		if len(api.responses) != 1 || api.responses[0].Data.Flags != discordgo.MessageFlagsEphemeral {
			t.Errorf("%s: expected one ephemeral reply", name)
		}
	}
}
