package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestGUICancelBeforeHistoryStoreExists(t *testing.T) {
	oldJobs, oldToken := historyJobs, guiToken
	historyJobs = nil
	guiToken = "test-token"
	t.Cleanup(func() { historyJobs, guiToken = oldJobs, oldToken })

	request := httptest.NewRequest(http.MethodPost, "/api/history/cancel", strings.NewReader(`{"channelId":"123"}`))
	request.Header.Set("X-DDG-GUI-Token", guiToken)
	recorder := httptest.NewRecorder()
	guiCancelHistoryHandler(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cancel before runtime initialization returned %d, want 404", recorder.Code)
	}
}

func TestGUIAddressRejectsInvalidPorts(t *testing.T) {
	for _, address := range []string{"localhost:abc", "127.0.0.1:-1", "[::1]:65536", "localhost:+80"} {
		if err := validateLocalGUIAddress(address); err == nil {
			t.Errorf("accepted invalid GUI address %q", address)
		}
	}
}

func TestDownloadLinkLogHandlesNilFirstEmbed(t *testing.T) {
	oldConfig := config
	oldSession := currentDiscordSession()
	config = defaultConfiguration()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession) })
	root := t.TempDir()
	noSubfolders := []string{}
	logFilename := "channel.txt"
	logLine := "{{embedDesc}}"
	source := configurationSource{
		ChannelID: "channel",
		LogLinks: &configurationSourceLog{
			Destination: root, Subfolders: &noSubfolders, FilenameFormat: &logFilename, LineContent: &logLine,
		},
	}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	message := &discordgo.Message{
		ChannelID: "channel",
		Author:    &discordgo.User{ID: "user"},
		Embeds:    []*discordgo.MessageEmbed{nil},
	}
	request := downloadRequestStruct{
		InputURL: "not-a-url", Path: root, Message: message,
		FileTime: time.Now(), StartTime: time.Now(),
	}
	request.handleDownload()
	if _, err := os.Stat(filepath.Join(root, "channel.txt")); err != nil {
		t.Fatalf("link log not written for message with nil embed: %v", err)
	}
	message.Embeds = []*discordgo.MessageEmbed{nil, {Description: "available description"}}
	request.handleDownload()
	logged, err := os.ReadFile(filepath.Join(root, "channel.txt"))
	if err != nil || !strings.Contains(string(logged), "available description") {
		t.Fatalf("link log did not use first available embed: %q, %v", logged, err)
	}
	setDiscordSession(nil)
	message.Embeds = nil
	request.handleDownload()
}
