package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestHistoryStatusDoesNotDiscloseJobsToNonAdmin(t *testing.T) {
	oldConfig, oldSession, oldJobs, oldSelfbot := config, currentDiscordSession(), historyJobs, selfbot
	config = defaultConfiguration()
	config.AllowGeneralCommands = true
	config.Admins = []string{"trusted-admin"}
	historyJobs = newHistoryJobStore()
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	var sentBodies []string
	session.Client.Transport = auditRoundTripper(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		sentBodies = append(sentBodies, string(body))
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"reply","channel_id":"123"}`))}, nil
	})
	setDiscordSession(session)
	selfbot = true // allow replies without a live guild permission lookup
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession); historyJobs = oldJobs; selfbot = oldSelfbot })
	message := &discordgo.Message{ID: "command", ChannelID: "123", Author: &discordgo.User{ID: "visitor"}, Content: "!history status"}
	if err := handleCommands().FindAndExecute(session, "!", "bot", message); err != nil {
		t.Fatal(err)
	}
	for _, body := range sentBodies {
		if strings.Contains(body, "CURRENT HISTORY JOBS") {
			t.Fatal("history status was disclosed to a non-administrator")
		}
	}
	sentBodies = nil
	message.Author.ID = "trusted-admin"
	if err := handleCommands().FindAndExecute(session, "!", "bot", message); err != nil {
		t.Fatal(err)
	}
	for _, body := range sentBodies {
		if strings.Contains(body, "CURRENT HISTORY JOBS") {
			return
		}
	}
	t.Fatal("history status was not sent to an administrator")
}

func TestMessageLoggingWorksWhileDiscordSessionIsUnavailable(t *testing.T) {
	oldConfig, oldSession := config, currentDiscordSession()
	config = defaultConfiguration()
	config.MessageOutput = false
	destination := t.TempDir()
	source := configurationSource{ChannelID: "channel", LogMessages: &configurationSourceLog{Destination: destination}}
	sourceDefault(&source)
	emptySubfolders := []string{}
	source.LogMessages.Subfolders = &emptySubfolders
	filename := "channel.txt"
	source.LogMessages.FilenameFormat = &filename
	config.Channels = []configurationSource{source}
	setDiscordSession(nil)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession) })
	message := &discordgo.Message{ID: "123", ChannelID: "channel", Author: &discordgo.User{ID: "author"}, Content: "hello"}
	handleMessageResult(message, nil, false, true)
	contents, err := os.ReadFile(filepath.Join(destination, "channel.txt"))
	if err != nil {
		t.Fatalf("message log was not created during reconnect: %v", err)
	}
	if !strings.Contains(string(contents), "hello") {
		t.Fatalf("message content missing from log: %q", contents)
	}
}

func TestHistoryCacheWipeAliasContainingAllIsNotGlobal(t *testing.T) {
	oldConfig, oldSession, oldCache := config, currentDiscordSession(), pathCacheHistory
	config = defaultConfiguration()
	config.AllowGeneralCommands = true
	config.Admins = []string{"admin"}
	alias := "ballroom"
	config.Channels = []configurationSource{{ChannelID: "123", Alias: &alias}}
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	pathCacheHistory = filepath.Join(t.TempDir(), "history")
	t.Cleanup(func() {
		config, pathCacheHistory = oldConfig, oldCache
		setDiscordSession(oldSession)
	})
	if err := os.MkdirAll(pathCacheHistory, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"123.json", "other.json"} {
		if err := os.WriteFile(filepath.Join(pathCacheHistory, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	message := &discordgo.Message{
		ChannelID: "123", Author: &discordgo.User{ID: "admin"},
		Content: "!history cachewipe ballroom",
	}
	if err := handleCommands().FindAndExecute(session, "!", "bot", message); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(pathCacheHistory, "other.json")); err != nil {
		t.Fatalf("unrelated channel cache was removed by an alias containing all: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pathCacheHistory, "123.json")); !os.IsNotExist(err) {
		t.Fatalf("selected channel cache was not removed: %v", err)
	}
}

func TestHistoryCancellationSurvivesChannelLookupFailure(t *testing.T) {
	oldSession, oldJobs := currentDiscordSession(), historyJobs
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	historyJobs = newHistoryJobStore()
	historyJobs.Set("missing-channel", historyJob{Status: historyStatusRunning})
	t.Cleanup(func() { setDiscordSession(oldSession); historyJobs = oldJobs })

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	session.Client.Transport = auditRoundTripper(func(*http.Request) (*http.Response, error) {
		close(requestStarted)
		<-releaseRequest
		return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"Unknown Channel","code":10003}`))}, nil
	})
	finished := make(chan struct{})
	go func() { handleHistory(nil, "missing-channel", "", ""); close(finished) }()
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		close(releaseRequest)
		t.Fatal("history channel request did not start")
	}
	if found, changed := historyJobs.RequestCancel("missing-channel"); !found || !changed {
		close(releaseRequest)
		t.Fatal("running history job could not be cancelled")
	}
	close(releaseRequest)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("history channel lookup did not finish")
	}
	job, _ := historyJobs.Get("missing-channel")
	if job.Status != historyStatusAbortCompleted {
		t.Fatalf("channel lookup error erased cancellation: status = %v", job.Status)
	}
}

func TestTistoryPageResolverWorksWhenHEADIsRejected(t *testing.T) {
	getCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		getCount++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html><body><div id=content></div></body></html>"))
	}))
	defer server.Close()
	if _, err := getPossibleTistorySiteUrls(server.URL); err != nil {
		t.Fatalf("Tistory resolver failed when GET is available but HEAD is rejected: %v", err)
	}
	if getCount != 1 {
		t.Fatalf("Tistory resolver made %d GET requests, want 1", getCount)
	}
}
