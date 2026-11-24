package config

import "testing"

func TestParseServices(t *testing.T) {
	got, err := ParseServices(" Seatbit=https://example.com , Floorbit=https://status.example.com/health,")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Seatbit" || got[1].URL != "https://status.example.com/health" {
		t.Fatalf("unexpected %+v", got)
	}
	for _, bad := range []string{"Seatbit", "=https://x", "X=ftp://y"} {
		if _, err := ParseServices(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestFromEnvRequired(t *testing.T) {
	t.Setenv("PLUMB_DISCORD_TOKEN", "")
	t.Setenv("PLUMB_GUILD_ID", "1")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected missing-settings error")
	}
	t.Setenv("PLUMB_DISCORD_TOKEN", "t")
	t.Setenv("PLUMB_ALERT_CHANNEL_ID", "2")
	t.Setenv("PLUMB_PLATFORM_ROLE_ID", "3")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != ":8080" || c.StatusAddr != ":8081" || !c.PingOnCritical {
		t.Fatalf("defaults wrong: %+v", c)
	}
}
