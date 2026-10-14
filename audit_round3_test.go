package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestDownloadFinishesWhenDiscordDisconnects(t *testing.T) {
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	oldSession := currentDiscordSession()
	setDiscordSession(nil)
	t.Cleanup(func() { setDiscordSession(oldSession) })
	config.SaveTextFiles = true
	config.Subfolders = nil
	config.FilenameFormat = "{{file}}"
	source := configurationSource{ChannelID: "channel", Destination: filepath.Join(root, "downloads")}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("offline media"))
	}))
	defer server.Close()
	request := downloadRequestStruct{
		InputURL: server.URL + "/file.txt", Path: source.Destination,
		Message:    &discordgo.Message{ID: "123", ChannelID: "channel", Author: &discordgo.User{ID: "author"}},
		HistoryCmd: true, FileTime: time.Now(), StartTime: time.Now(),
	}
	status, _ := request.tryDownload()
	if status.Status != downloadSuccess {
		t.Fatalf("download failed after Discord disconnected: %+v", status)
	}
}

func TestMessageOutputWorksWhileDiscordSessionIsUnavailable(t *testing.T) {
	oldConfig, oldSession := config, currentDiscordSession()
	config = defaultConfiguration()
	config.MessageOutput = true
	source := configurationSource{ChannelID: "channel"}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	setDiscordSession(nil)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession) })
	message := &discordgo.Message{ID: "123", ChannelID: "channel", Author: &discordgo.User{ID: "author"}, Content: "hello"}
	handleMessageResult(message, nil, false, true)
	if got := getChannelLabel("channel", &discordgo.Channel{Name: "cached"}); got != "cached" {
		t.Fatalf("provided channel data was ignored during disconnect: %q", got)
	}
	if sourceName, channelName := channelDisplay("channel"); sourceName != "UNKNOWN" || channelName != "channel" {
		t.Fatalf("unexpected offline channel display: %q, %q", sourceName, channelName)
	}
}

func TestHistoryChannelLookupFailsGracefullyWhileDisconnected(t *testing.T) {
	oldSession, oldJobs := currentDiscordSession(), historyJobs
	setDiscordSession(nil)
	historyJobs = newHistoryJobStore()
	historyJobs.Set("channel", historyJob{Status: historyStatusRunning})
	t.Cleanup(func() { setDiscordSession(oldSession); historyJobs = oldJobs })
	if result := handleHistory(nil, "channel", "", ""); result != -1 {
		t.Fatalf("disconnected history result = %d, want error", result)
	}
	job, _ := historyJobs.Get("channel")
	if job.Status != historyStatusErrorRequesting {
		t.Fatalf("disconnected history status = %v, want request error", job.Status)
	}
}

func TestCategoryLabelUsesNamedParentOfUnnamedChannel(t *testing.T) {
	oldSession := currentDiscordSession()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	t.Cleanup(func() { setDiscordSession(oldSession) })
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{
		{ID: "category", GuildID: "guild", Name: "Photos", Type: discordgo.ChannelTypeGuildCategory},
		{ID: "child", GuildID: "guild", ParentID: "category", Type: discordgo.ChannelTypeGuildText},
	}}); err != nil {
		t.Fatal(err)
	}
	if got := getCategoryLabel("child"); got != "Photos" {
		t.Fatalf("category label = %q, want Photos", got)
	}
}
