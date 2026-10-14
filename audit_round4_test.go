package main

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestRegisteredChannelsDuringDiscordReconnect(t *testing.T) {
	oldConfig, oldSession, oldSelfbot := config, currentDiscordSession(), selfbot
	config = defaultConfiguration()
	serverIDs := []string{"guild"}
	config.Servers = []configurationSource{{ServerIDs: &serverIDs}}
	selfbot = true
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession); selfbot = oldSelfbot })

	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 1000; i++ {
			setDiscordSession(nil)
			setDiscordSession(session)
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 1000; i++ {
			getAllRegisteredChannels()
		}
	}()
	close(start)
	workers.Wait()
}

func TestAdminNotificationsDuringDiscordReconnect(t *testing.T) {
	oldConfig, oldSession := config, currentDiscordSession()
	config = defaultConfiguration()
	logErrors := true
	config.AdminChannels = []configurationAdminChannel{{ChannelID: "admin", LogErrors: &logErrors}}
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	session.Client.Transport = auditRoundTripper(func(request *http.Request) (*http.Response, error) {
		body := `{"id":"reply","channel_id":"admin"}`
		if request.Method == http.MethodGet {
			close(lookupStarted)
			<-releaseLookup
			body = `{"id":"admin","type":1}`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	setDiscordSession(session)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession) })
	finished := make(chan struct{})
	go func() { sendErrorMessage("test error"); close(finished) }()
	select {
	case <-lookupStarted:
	case <-time.After(3 * time.Second):
		close(releaseLookup)
		t.Fatal("permission lookup did not start")
	}
	setDiscordSession(nil)
	close(releaseLookup)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("error notification did not finish")
	}
}

func TestPingCompletesDuringDiscordReconnect(t *testing.T) {
	oldConfig, oldSession, oldSelfbot := config, currentDiscordSession(), selfbot
	config = defaultConfiguration()
	config.AllowGeneralCommands = true
	selfbot = true
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	replyStarted := make(chan struct{})
	releaseReply := make(chan struct{})
	var firstReply sync.Once
	session.Client.Transport = auditRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost {
			firstReply.Do(func() { close(replyStarted) })
			<-releaseReply
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"reply","channel_id":"channel"}`))}, nil
	})
	setDiscordSession(session)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession); selfbot = oldSelfbot })
	finished := make(chan error, 1)
	go func() {
		finished <- handleCommands().FindAndExecute(session, "!", "bot", &discordgo.Message{
			ID: "command", ChannelID: "channel", Content: "!ping", Author: &discordgo.User{ID: "author"},
		})
	}()
	select {
	case <-replyStarted:
	case <-time.After(3 * time.Second):
		close(releaseReply)
		t.Fatal("ping reply did not start")
	}
	setDiscordSession(nil)
	close(releaseReply)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ping command did not finish")
	}
}

func TestHistoryCommandUsesItsEventSessionAfterDisconnect(t *testing.T) {
	oldConfig, oldSession, oldJobs := config, currentDiscordSession(), historyJobs
	config = defaultConfiguration()
	config.AllowGeneralCommands = true
	config.Admins = []string{"admin"}
	historyJobs = newHistoryJobStore()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "123", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "123"}}}); err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	router := handleCommands()
	setDiscordSession(nil)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession); historyJobs = oldJobs })
	message := &discordgo.Message{ID: "command", ChannelID: "channel", Author: &discordgo.User{ID: "admin"}, Content: "!history cancel 123"}
	if err := router.FindAndExecute(session, "!", "bot", message); err != nil {
		t.Fatal(err)
	}
}
