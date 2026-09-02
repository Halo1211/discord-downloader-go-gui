package main

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestDiscordAPIUsesV10Everywhere(t *testing.T) {
	configureDiscordAPI()
	if discordgo.APIVersion != "10" {
		t.Fatalf("APIVersion = %q, want 10", discordgo.APIVersion)
	}
	for name, endpoint := range map[string]string{
		"api":      discordgo.EndpointAPI,
		"gateway":  discordgo.EndpointGateway,
		"channels": discordgo.EndpointChannels,
		"guilds":   discordgo.EndpointGuilds,
		"users":    discordgo.EndpointUsers,
	} {
		if !strings.Contains(endpoint, "/api/v10/") {
			t.Errorf("%s endpoint still uses an older API: %s", name, endpoint)
		}
	}
}
