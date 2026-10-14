package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rivo/duplo"
)

func auditDiscordState(t *testing.T) *discordgo.Session {
	t.Helper()
	oldConfig, oldSession := config, currentDiscordSession()
	config = defaultConfiguration()
	config.MessageOutput = false
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	setDiscordSession(session)
	t.Cleanup(func() { config = oldConfig; setDiscordSession(oldSession) })
	return session
}

func TestAuditEmptyAuthorIDDoesNotGainBotPrivileges(t *testing.T) {
	previousConfig, previousUser := config, currentDiscordUser()
	config = defaultConfiguration()
	config.Admins = []string{"trusted-admin"}
	config.AllowGeneralCommands = false
	allowCommands := false
	config.Channels = []configurationSource{{ChannelID: "channel", AllowCommands: &allowCommands}}
	setDiscordUser(nil)
	t.Cleanup(func() { config = previousConfig; setDiscordUser(previousUser) })

	message := &discordgo.Message{ChannelID: "channel", Author: &discordgo.User{}}
	if isBotAdmin(message) {
		t.Fatal("an empty author ID was accepted as an administrator before login")
	}
	if isCommandableChannel(message) {
		t.Fatal("an empty author ID was allowed to run commands before login")
	}
}

func TestAuditConcurrentDownloadDoesNotSkipFilePendingDatabaseCommit(t *testing.T) {
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	config.SaveTextFiles = true
	databaseMu.Lock()
	myDB = nil // force the first database commit to fail after file promotion
	databaseMu.Unlock()

	var served atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("concurrent download fixture"))
		served.Add(1)
	}))
	defer server.Close()

	destination := filepath.Join(root, "downloads")
	request := downloadRequestStruct{
		InputURL: server.URL + "/same.txt", Path: destination,
		EmojiCmd: true, FileTime: time.Now(), StartTime: time.Now(),
	}
	results := make(chan downloadStatusStruct, 2)
	databaseMu.Lock()
	locked := true
	defer func() {
		if locked {
			databaseMu.Unlock()
		}
	}()
	go func() { status, _ := request.tryDownload(); results <- status }()
	path := filepath.Join(destination, "same.txt")
	deadline := time.After(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first download did not promote its file before database commit")
		case <-time.After(5 * time.Millisecond):
		}
	}
	go func() { status, _ := request.tryDownload(); results <- status }()
	deadline = time.After(3 * time.Second)
	for served.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("second download did not receive its response")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Neither request may finish while the first database commit is held.
	// Previously the second returned "duplicate" for a file not yet committed.
	select {
	case status := <-results:
		t.Fatalf("download finished before database commit: %+v", status)
	case <-time.After(200 * time.Millisecond):
	}
	databaseMu.Unlock()
	locked = false
	for i := 0; i < 2; i++ {
		select {
		case status := <-results:
			if status.Status == downloadSkippedDuplicate {
				t.Fatal("download was skipped for a file later removed after database failure")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("concurrent download did not finish")
		}
	}
}

func TestAuditUnreadableExistingSettingsAreNotReportedAsNew(t *testing.T) {
	oldBase, oldFile, oldGUIEnabled := configFileBase, configFile, guiFlagEnabled
	oldYAML, oldJSONC := configFileYaml, configFileC
	t.Cleanup(func() {
		configFileBase, configFile, guiFlagEnabled = oldBase, oldFile, oldGUIEnabled
		configFileYaml, configFileC = oldYAML, oldJSONC
	})
	configFileBase = filepath.Join(t.TempDir(), "settings")
	guiFlagEnabled = true
	if err := os.Mkdir(configFileBase+".json", 0700); err != nil {
		t.Fatal(err)
	}
	err := loadConfig()
	if err == nil || strings.Contains(err.Error(), "was created") {
		t.Fatalf("unreadable existing settings reported as newly created: %v", err)
	}
}

func TestAuditFailedSettingsCreationIsNotReportedAsCreated(t *testing.T) {
	oldBase, oldFile, oldGUIEnabled := configFileBase, configFile, guiFlagEnabled
	oldYAML, oldJSONC := configFileYaml, configFileC
	t.Cleanup(func() {
		configFileBase, configFile, guiFlagEnabled = oldBase, oldFile, oldGUIEnabled
		configFileYaml, configFileC = oldYAML, oldJSONC
	})
	configFileBase = filepath.Join(t.TempDir(), "missing-parent", "settings")
	guiFlagEnabled = true
	err := loadConfig()
	if err == nil || strings.Contains(err.Error(), "was created") {
		t.Fatalf("failed settings creation reported as successful: %v", err)
	}
}

func TestAuditEmbedColorBeforeDiscordLoginDoesNotPanic(t *testing.T) {
	previousConfig, previousSession, previousUser := config, currentDiscordSession(), currentDiscordUser()
	config = defaultConfiguration()
	setDiscordSession(nil)
	setDiscordUser(nil)
	t.Cleanup(func() {
		config = previousConfig
		setDiscordSession(previousSession)
		setDiscordUser(previousUser)
	})
	for _, configured := range []*string{nil, func() *string { value := "role"; return &value }()} {
		config.EmbedColor = configured
		color := getEmbedColor("channel")
		if color < 0 || color > 0xffffff {
			t.Fatalf("invalid fallback embed color: %#x", color)
		}
	}
}

func TestAuditServerSourceUsesChannelGuildAndKeepsBlacklist(t *testing.T) {
	session := auditDiscordState(t)
	channel := &discordgo.Channel{ID: "channel", GuildID: "guild", ParentID: "category"}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{channel}}); err != nil {
		t.Fatal(err)
	}
	message := &discordgo.Message{ChannelID: "channel", GuildID: "guild"}
	config.Servers = []configurationSource{{ServerID: "guild", Destination: "server"}}
	config.All = &configurationSource{Destination: "all"}
	guild, err := session.State.Guild("guild")
	if err != nil {
		t.Fatal(err)
	}
	guild.Channels = nil // a cached channel can exist before the guild's list refreshes
	if got := getSource(message).Destination; got != "server" {
		t.Fatalf("server source missed cached guild channel: %q", got)
	}
	guild.Channels = []*discordgo.Channel{channel}
	blocked := []string{"channel"}
	config.Servers[0].ServerBlacklist = &blocked
	if got := getSource(message); got != emptySourceConfig {
		t.Fatalf("server blacklist fell through to another source: %+v", got)
	}
}

func TestAuditHistoryCancellationCannotBeOverwrittenByWorkerStatus(t *testing.T) {
	for _, workerStatus := range []historyStatus{historyStatusRunning, historyStatusCompletedNoMoreMessages, historyStatusErrorRequesting} {
		store := newHistoryJobStore()
		store.Set("channel", historyJob{Status: historyStatusRunning})
		if found, changed := store.RequestCancel("channel"); !found || !changed {
			t.Fatal("failed to request cancellation of running history job")
		}
		store.SetStatus("channel", workerStatus)
		job, _ := store.Get("channel")
		want := historyStatusAbortRequested
		if workerStatus != historyStatusRunning {
			want = historyStatusAbortCompleted
		}
		if job.Status != want {
			t.Errorf("worker status %v overwrote cancellation with %v; want %v", workerStatus, job.Status, want)
		}
	}
}

func TestAuditReplyEmbedHandlesMissingMessageAuthor(t *testing.T) {
	previousSession, previousSelfbot := currentDiscordSession(), selfbot
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	session.Client.Transport = auditRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Status: "403 Forbidden", Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	setDiscordSession(session)
	selfbot = true
	t.Cleanup(func() { setDiscordSession(previousSession); selfbot = previousSelfbot })
	_, _ = replyEmbed(&discordgo.Message{ChannelID: "channel", ID: "message"}, "title", "body")
}

func TestAuditStickerSyncUsesDedicatedGuildStickerList(t *testing.T) {
	previousConfig, previousSession := config, currentDiscordSession()
	config = defaultConfiguration()
	servers := []string{"guild"}
	config.StickersServers = &servers
	destination := t.TempDir()
	config.StickersDestination = &destination
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	session.Client.Transport = auditRoundTripper(func(request *http.Request) (*http.Response, error) {
		body := `{"id":"guild","name":"Guild"}`
		if strings.HasSuffix(request.URL.Path, "/guilds/guild/stickers") {
			listed = true
			body = `[]`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	setDiscordSession(session)
	t.Cleanup(func() { config = previousConfig; setDiscordSession(previousSession) })
	downloadDiscordStickers()
	if !listed {
		t.Fatal("sticker sync relied on the optional guild.stickers field instead of listing stickers")
	}
}

func TestAuditDiscordGettersUseCacheBeforeNetwork(t *testing.T) {
	session := auditDiscordState(t)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	session.Client.Transport = auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		t.Error("cached lookup contacted Discord")
		return nil, fmt.Errorf("unexpected network call")
	})
	if _, err := getChannel("channel"); err != nil {
		t.Fatal(err)
	}
	if _, err := getServer("guild"); err != nil {
		t.Fatal(err)
	}
}

func TestAuditDiscordGettersBeforeLoginDoNotPanic(t *testing.T) {
	auditDiscordState(t)
	setDiscordSession(nil)
	defer func() {
		if value := recover(); value != nil {
			t.Fatalf("getter before login panicked: %v", value)
		}
	}()
	if _, err := getChannel("channel"); err == nil {
		t.Fatal("missing session did not return an error")
	}
	if _, err := getServer("guild"); err == nil {
		t.Fatal("missing session did not return an error")
	}
}

func TestAuditHistoryRecoveryDoesNotExecuteCommands(t *testing.T) {
	session := auditDiscordState(t)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	session.Client.Transport = auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[{"id":"100","channel_id":"channel","content":"ddg exit","author":{"id":"author"}}]`)), Header: make(http.Header)}, nil
	})
	source := configurationSource{ChannelID: "channel", Destination: t.TempDir()}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	oldCommands := botCommands
	botCommands = nil
	t.Cleanup(func() { botCommands = oldCommands })
	defer func() {
		if value := recover(); value != nil {
			t.Fatalf("scanning history executed a recovered command: %v", value)
		}
	}()
	handleMessage(&discordgo.Message{ID: "100", ChannelID: "channel", GuildID: "guild", Author: &discordgo.User{ID: "author"}}, nil, false, true)
}

func TestAuditHistoryWaitsForUnavailableSocialResolver(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	auditDiscordState(t)
	setDiscordSession(nil)
	oldTwitter, oldInstagram := twitterConnected.Load(), instagramConnected.Load()
	twitterConnected.Store(false)
	instagramConnected.Store(false)
	t.Cleanup(func() { twitterConnected.Store(oldTwitter); instagramConnected.Store(oldInstagram) })
	source := configurationSource{ChannelID: "channel", Destination: t.TempDir()}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	for _, content := range []string{"https://www.instagram.com/p/Abc123/", "https://x.com/alice/status/123456789"} {
		_, complete := handleMessageResult(&discordgo.Message{ID: "100", ChannelID: "channel", Content: content, Author: &discordgo.User{ID: "author"}}, nil, false, true)
		if complete {
			t.Errorf("history permanently skipped unresolved social post %s", content)
		}
	}
}

type auditRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip auditRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestAuditFlickrAlbumPaginatesAndEscapesAPIKey(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	auditDiscordState(t)
	previousClient := jsonHTTPClient
	t.Cleanup(func() { jsonHTTPClient = previousClient })
	config.Credentials.FlickrApiKey = "test+key&suffix"
	jsonHTTPClient = &http.Client{Transport: auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		if query.Get("api_key") != config.Credentials.FlickrApiKey {
			t.Errorf("API key corrupted in query")
		}
		body := ""
		if query.Get("method") == "flickr.people.findByUsername" {
			if query.Get("username") != "testuser" {
				t.Errorf("wrong Flickr username lookup")
			}
			body = `{"stat":"ok","user":{"nsid":"123@N01"}}`
		} else if query.Get("method") == "flickr.photosets.getPhotos" {
			if query.Get("user_id") != "123@N01" {
				t.Errorf("album request omitted Flickr user_id")
			}
			page := query.Get("page")
			if page == "" {
				page = "1"
			}
			body = fmt.Sprintf(`{"stat":"ok","photoset":{"page":%s,"pages":2,"photo":[{"id":"%s"}]}}`, page, page)
		} else {
			body = fmt.Sprintf(`{"stat":"ok","sizes":{"size":[{"label":"Original","width":100,"height":100,"source":"https://example.com/%s.png"}]}}`, query.Get("photo_id"))
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	links, err := getFlickrAlbumUrls("https://www.flickr.com/photos/testuser/albums/123")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d photos, want both album pages", len(links))
	}
}

func TestAuditFlickrRejectsAPIErrorResponse(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	auditDiscordState(t)
	previousClient := jsonHTTPClient
	t.Cleanup(func() { jsonHTTPClient = previousClient })
	config.Credentials.FlickrApiKey = "test-key"
	jsonHTTPClient = &http.Client{Transport: auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"stat":"fail","message":"Invalid API key"}`)), Header: make(http.Header)}, nil
	})}
	if _, err := getFlickrAlbumUrls("https://www.flickr.com/photos/testuser/albums/123"); err == nil {
		t.Fatal("Flickr API error treated as an empty successful album")
	}
	if _, err := getFlickrPhotoUrls("https://www.flickr.com/photos/123@N00/456"); err == nil {
		t.Fatal("Flickr API error returned a blank media URL")
	}
}

func TestAuditHTTPJSONErrorsDoNotExposeAPIKey(t *testing.T) {
	previousClient := jsonHTTPClient
	t.Cleanup(func() { jsonHTTPClient = previousClient })
	jsonHTTPClient = &http.Client{Transport: auditRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Body: io.NopCloser(strings.NewReader("denied")), Header: make(http.Header)}, nil
	})}
	var target map[string]interface{}
	secretURL := "https://www.flickr.com/services/rest/?api_key=private-api-key&photo_id=123"
	for _, get := range []func(string, interface{}) error{getJSON, func(input string, value interface{}) error { return getJSONwithHeaders(input, value, nil) }} {
		if err := get(secretURL, &target); err == nil || strings.Contains(err.Error(), "private-api-key") {
			t.Errorf("HTTP error exposed API key: %v", err)
		}
	}
}

func TestAuditFileLoggingStillFeedsGUIConsole(t *testing.T) {
	root := t.TempDir()
	oldConfig, oldBase, oldFile, oldJSONC, oldYAML, oldWriter := config, configFileBase, configFile, configFileC, configFileYaml, log.Writer()
	t.Cleanup(func() {
		_ = configureLogOutput("")
		config, configFileBase, configFile, configFileC, configFileYaml = oldConfig, oldBase, oldFile, oldJSONC, oldYAML
		applyConfigurationPaths(config)
		applyConfigurationReaction(config)
		log.SetOutput(oldWriter)
	})
	candidate := defaultConfiguration()
	candidate.LogOutput = filepath.Join(root, "output.log")
	candidate.OverwriteCachePath = filepath.Join(root, "cache")
	candidate.LogSettings = false
	candidate.Credentials.Token = "test-token"
	configFileBase = filepath.Join(root, "settings")
	content, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFileBase+".json", content, 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	_, cursor := guiRuntimeLogs.after(0)
	log.Print("audit-file-log-marker")
	entries, _ := guiRuntimeLogs.after(cursor)
	for _, entry := range entries {
		if strings.Contains(entry.Text, "audit-file-log-marker") {
			return
		}
	}
	t.Fatal("enabling file output disconnected the GUI log console")
}

func TestAuditURLRewritingDoesNotAlterOtherHostsOrQueries(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	auditDiscordState(t)
	setDiscordSession(nil)
	input := "https://example.com/x.com/file.png?return=//x.com&alias=fxtwitter.com"
	links := getParsedLinks(input, &discordgo.Message{ChannelID: "channel"})
	if _, ok := links[input]; !ok {
		t.Fatalf("unrelated URL was rewritten: %v", links)
	}
}

func TestAuditHistoryDoesNotPrunePendingRealTimeDownload(t *testing.T) {
	auditDiscordState(t)
	setDiscordSession(nil)
	oldManager := currentDownloadManager()
	m := &downloadManager{inFlight: make(map[string]struct{})}
	setDownloadManager(m)
	t.Cleanup(func() { setDownloadManager(oldManager) })
	link := "https://example.com/image.png"
	if !m.reserve(link, "channel") {
		t.Fatal("reservation failed")
	}
	defer m.release(downloadQueueKey(link, "channel"))
	if len(pruneCompletedLinks(map[string]string{link: ""}, &discordgo.Message{ChannelID: "channel"})) != 1 {
		t.Fatal("pending work was treated as completed; a failure could be skipped by history")
	}
}

func TestAuditHistoryWaitsForPendingDownloadOutcome(t *testing.T) {
	auditDiscordState(t)
	setDiscordSession(nil)
	previous := currentDownloadManager()
	startDownloadManager(1, 1)
	manager := currentDownloadManager()
	t.Cleanup(func() { manager.stopAndWait(); setDownloadManager(previous) })
	input := "invalid-url"
	if !manager.reserve(input, "0") {
		t.Fatal("reservation failed")
	}
	released := false
	defer func() {
		if !released {
			manager.release(downloadQueueKey(input, "0"))
		}
	}()
	result := make(chan downloadStatusStruct, 1)
	go func() {
		status, _ := runHistoryDownload(downloadRequestStruct{InputURL: input, EmojiCmd: true})
		result <- status
	}()
	select {
	case status := <-result:
		t.Fatalf("history prematurely completed: %v", status)
	case <-time.After(25 * time.Millisecond):
	}
	manager.release(downloadQueueKey(input, "0"))
	released = true
	select {
	case status := <-result:
		if status.Status != downloadFailedInvalidSource {
			t.Fatalf("status=%v, want original failure to be retried", status)
		}
	case <-time.After(time.Second):
		t.Fatal("history did not resume")
	}
}

func TestAuditSourceDuploThresholdOverridesGlobal(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	session := auditDiscordState(t)
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	oldCatalog := duploCatalog
	duploCatalog = duplo.New()
	t.Cleanup(func() { duploCatalog = oldCatalog })
	config.Duplo = true
	config.DuploThreshold = 100
	config.Subfolders = nil
	config.FilenameFormat = "{{file}}"
	threshold := -1e9
	source := configurationSource{ChannelID: "channel", Destination: filepath.Join(root, "downloads"), DuploThreshold: &threshold}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	var contents bytes.Buffer
	if err := png.Encode(&contents, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(contents.Bytes()) }))
	defer server.Close()
	for _, filename := range []string{"first.png", "second.png"} {
		status, _ := (downloadRequestStruct{InputURL: server.URL + "/" + filename, Path: source.Destination, Filename: filename,
			Message: &discordgo.Message{ID: "100", ChannelID: "channel", Author: &discordgo.User{ID: "author"}}, FileTime: time.Now(), StartTime: time.Now(), HistoryCmd: true}).tryDownload()
		if status.Status != downloadSuccess {
			t.Errorf("%s status=%v, want success with source threshold", filename, status.Status)
		}
	}
}

func TestAuditLegacyMonotonicTimestampIsReadable(t *testing.T) {
	installDownloadTestDatabase(t, t.TempDir())
	now := time.Now()
	id, err := myDB.Use("Downloads").Insert(map[string]interface{}{"Time": now.String(), "URL": "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if got := dbFindDownloadByID(id); got == nil || !got.Time.Equal(now) {
		t.Fatalf("legacy timestamp lost: %+v", got)
	}
}

func TestAuditGUIRejectsMissingTickerIntervals(t *testing.T) {
	for _, field := range []string{"checkup", "presence", "connection", "history"} {
		candidate := defaultConfiguration()
		switch field {
		case "checkup":
			candidate.CheckupRate = 0
		case "presence":
			candidate.PresenceRefreshRate = -1
		case "connection":
			candidate.ConnectionCheckRate = 0
		case "history":
			candidate.HistoryManagerRate = 0
		}
		if guiValidateConfiguration(candidate) == nil {
			t.Errorf("accepted invalid %s interval", field)
		}
	}
}

func TestAuditIncompleteEventLogCanUseEmptySession(t *testing.T) {
	auditDiscordState(t)
	setDiscordSession(nil)
	message := &discordgo.Message{ID: "100", ChannelID: "channel", Content: "message"}
	if fixed := fixMessage(message, false); fixed != message {
		t.Fatal("changed complete message")
	}
}

func TestAuditNullLinkInputsAreSafe(t *testing.T) {
	if got := getLinksByMessage(nil); len(got) != 0 {
		t.Fatal("nil message generated links")
	}
}

func TestAuditBlockedMessageCannotBeReallowed(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	session := auditDiscordState(t)
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(imageData.Bytes()) }))
	defer server.Close()
	blockedPhrase, allowedPhrase := []string{"deny"}, []string{"allow"}
	blockedRole, allowedRole := []string{"blocked"}, []string{"allowed"}
	allowedUsers := []string{"author"}
	for _, scenario := range []struct {
		name    string
		filters configurationSourceFilters
		content string
		member  *discordgo.Member
	}{
		{"phrase versus user", configurationSourceFilters{BlockedPhrases: &blockedPhrase, AllowedUsers: &allowedUsers}, "deny", nil},
		{"role versus role", configurationSourceFilters{BlockedRoles: &blockedRole, AllowedRoles: &allowedRole}, "", &discordgo.Member{Roles: []string{"blocked", "allowed"}}},
		{"independent allow lists", configurationSourceFilters{AllowedPhrases: &allowedPhrase, AllowedUsers: &[]string{"other"}}, "allow", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			source := configurationSource{ChannelID: "channel", Destination: filepath.Join(root, scenario.name), Filters: &scenario.filters}
			sourceDefault(&source)
			config.Channels = []configurationSource{source}
			message := &discordgo.Message{ID: "100", ChannelID: "channel", GuildID: "guild", Content: scenario.content, Author: &discordgo.User{ID: "author"}, Member: scenario.member,
				Attachments: []*discordgo.MessageAttachment{{URL: server.URL + "/file.png", Filename: "file.png"}}}
			if items := handleMessage(message, nil, false, true); len(items) != 0 {
				t.Errorf("blocked message downloaded %d items", len(items))
			}
		})
	}
}

func TestAuditBlockedFilenameCannotBeReallowed(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	session := auditDiscordState(t)
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(imageData.Bytes()) }))
	defer server.Close()
	blocked, allowed := []string{"file"}, []string{"file"}
	source := configurationSource{ChannelID: "channel", Destination: root, Filters: &configurationSourceFilters{BlockedFilenames: &blocked, AllowedFilenames: &allowed}}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	status, _ := (downloadRequestStruct{InputURL: server.URL + "/file.png", Filename: "file.png", Path: root, Message: &discordgo.Message{ID: "100", ChannelID: "channel", Author: &discordgo.User{ID: "author"}}, FileTime: time.Now(), StartTime: time.Now(), HistoryCmd: true}).tryDownload()
	if status.Status != downloadSkippedUnpermittedFilename {
		t.Errorf("blocked filename status=%v", status.Status)
	}
}

func TestAuditSnowflakeRejectsOutOfRangeDates(t *testing.T) {
	for _, date := range []string{"2014-01-01", "2300-01-01"} {
		if got := discordTimestampToSnowflake("2006-01-02", date); got != "" {
			t.Errorf("out-of-range date %s produced invalid snowflake %s", date, got)
		}
	}
}

func TestAuditGUIConfigRejectsEmptyTOTPReplacement(t *testing.T) {
	current, candidate := defaultConfiguration(), defaultConfiguration()
	secret, blank := "existing-totp", "  "
	current.Credentials.InstagramTOTP, candidate.Credentials.InstagramTOTP = &secret, &blank
	guiPreserveConfigurationSecrets(&candidate, current)
	if strings.TrimSpace(*candidate.Credentials.InstagramTOTP) != secret {
		t.Fatal("blank TOTP erased existing secret")
	}
}

func TestAuditDistinctMessageEditsAreProcessed(t *testing.T) {
	auditDiscordState(t)
	setDiscordSession(nil)
	oldEvents := messageEvents
	messageEvents = newMessageEventDeduper()
	t.Cleanup(func() { messageEvents = oldEvents })
	edited := time.Now()
	for _, content := range []string{"first edit", "second edit", "second edit"} {
		messageUpdate(nil, &discordgo.MessageUpdate{Message: &discordgo.Message{ID: "1", Content: content, EditedTimestamp: &edited}})
	}
	if len(messageEvents.seen) != 2 {
		t.Fatalf("accepted %d distinct edits, want 2", len(messageEvents.seen))
	}
}

func TestAuditEmptyEventEnvelopesDoNotPanic(t *testing.T) {
	for name, call := range map[string]func(){
		"create": func() { messageCreate(nil, &discordgo.MessageCreate{}) },
		"update": func() { messageUpdate(nil, &discordgo.MessageUpdate{}) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Fatalf("empty event panicked: %v", value)
				}
			}()
			call()
		})
	}
}

func TestAuditCategoryScopeAndThreadInheritance(t *testing.T) {
	session := auditDiscordState(t)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{
		{ID: "channel", GuildID: "guild", ParentID: "category-b"},
		{ID: "thread", GuildID: "guild", ParentID: "channel", Type: discordgo.ChannelTypeGuildPublicThread},
	}}); err != nil {
		t.Fatal(err)
	}
	blocked := []string{"channel"}
	config.Categories = []configurationSource{
		{CategoryID: "category-a", CategoryBlacklist: &blocked, Destination: "a"},
		{CategoryID: "category-b", Destination: "b"},
	}
	for _, id := range []string{"channel", "thread"} {
		if got := getSource(&discordgo.Message{ChannelID: id}); got.Destination != "b" {
			t.Errorf("source for %s = %q, want b", id, got.Destination)
		}
	}
}

func TestAuditAllServerBlacklistUsesChannelGuild(t *testing.T) {
	session := auditDiscordState(t)
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild"}}}); err != nil {
		t.Fatal(err)
	}
	config.All = &configurationSource{Destination: "all"}
	blocked := []string{"guild"}
	config.AllBlacklistServers = &blocked
	if got := getSource(&discordgo.Message{ChannelID: "channel"}); got != emptySourceConfig {
		t.Fatal("REST message without guild ID bypassed server blacklist")
	}
}

func TestAuditInheritedLogsReceiveDefaults(t *testing.T) {
	auditDiscordState(t)
	config.LogLinks = &configurationSourceLog{Destination: "links"}
	config.LogMessages = &configurationSourceLog{Destination: "messages"}
	var source configurationSource
	sourceDefault(&source)
	if source.LogLinks.LineContent == nil || source.LogLinks.LogDownloads == nil || source.LogMessages.LineContent == nil {
		t.Fatal("inherited log configuration is missing defaults")
	}
}

func TestAuditRawLinksIgnoreNullMedia(t *testing.T) {
	defer func() {
		if value := recover(); value != nil {
			t.Fatalf("null media panicked: %v", value)
		}
	}()
	message := &discordgo.Message{
		Attachments:      []*discordgo.MessageAttachment{nil},
		Embeds:           []*discordgo.MessageEmbed{nil},
		MessageSnapshots: []discordgo.MessageSnapshot{{Message: &discordgo.Message{Attachments: []*discordgo.MessageAttachment{nil}, Embeds: []*discordgo.MessageEmbed{nil}}}},
	}
	if got := getRawLinks(message); len(got) != 0 {
		t.Fatalf("got %d links for empty media", len(got))
	}
}

func TestAuditDatabaseTimestampRoundTrip(t *testing.T) {
	installDownloadTestDatabase(t, t.TempDir())
	now := time.Now()
	if err := dbInsertDownload(&downloadItem{URL: "https://example.com/photo.png", ChannelID: "channel", Time: now}); err != nil {
		t.Fatal(err)
	}
	items := dbFindDownloadByURL("https://example.com/photo.png")
	if len(items) != 1 || !items[0].Time.Equal(now) {
		t.Fatalf("timestamp did not round trip: %+v", items)
	}
}

func TestAuditGUISecretOmittedPreservesToken(t *testing.T) {
	candidate, current := defaultConfiguration(), defaultConfiguration()
	current.Credentials.Token = "existing-token"
	guiPreserveConfigurationSecrets(&candidate, current)
	if candidate.Credentials.Token != current.Credentials.Token {
		t.Fatal("omitting token replaces credential with the default placeholder")
	}
}

func TestAuditSourceLogDuplicateMatchesWholeRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.txt")
	if _, err := appendSourceLogLine(path, "\nhello world", true, true); err != nil {
		t.Fatal(err)
	}
	if added, err := appendSourceLogLine(path, "\nhello", true, true); err != nil || !added {
		t.Fatalf("distinct shorter record rejected: added=%v err=%v", added, err)
	}
	if added, err := appendSourceLogLine(path, "\nhello", true, true); err != nil || added {
		t.Fatalf("duplicate record accepted: added=%v err=%v", added, err)
	}
}

func TestAuditHistoryFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	if err := compileRegex(); err != nil {
		t.Fatal(err)
	}
	session := auditDiscordState(t)
	root := t.TempDir()
	installDownloadTestDatabase(t, root)
	oldHistory, oldCache := historyJobs, pathCacheHistory
	historyJobs, pathCacheHistory = newHistoryJobStore(), filepath.Join(root, "history")
	t.Cleanup(func() { historyJobs, pathCacheHistory = oldHistory, oldCache })
	failed, expectedBefore := true, ""
	var media bytes.Buffer
	if err := png.Encode(&media, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/channels/channel/messages" {
			if expectedBefore != "" && r.URL.Query().Get("before") != expectedBefore {
				t.Errorf("before cursor=%q, want %s", r.URL.Query().Get("before"), expectedBefore)
			}
			_, _ = w.Write([]byte(`[{"id":"100","channel_id":"channel","content":"","author":{"id":"author"},"attachments":[{"url":"` + server.URL + `/media.png","filename":"media.png"}]}]`))
			return
		}
		if r.URL.Path == "/media.png" {
			if failed {
				w.WriteHeader(http.StatusNotFound)
			} else {
				_, _ = w.Write(media.Bytes())
			}
			return
		}
		_, _ = w.Write([]byte(`{"threads":[],"has_more":false}`))
	}))
	defer server.Close()
	oldEndpoint := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() { discordgo.EndpointChannels = oldEndpoint })
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild", OwnerID: "bot", Channels: []*discordgo.Channel{{ID: "channel", GuildID: "guild", Type: discordgo.ChannelTypeGuildText}}}); err != nil {
		t.Fatal(err)
	}
	oldUser := currentDiscordUser()
	setDiscordUser(&discordgo.User{ID: "bot"})
	t.Cleanup(func() { setDiscordUser(oldUser) })
	session.State.User = &discordgo.User{ID: "bot"}
	config.SendAutoHistoryStatus = false
	config.OutputHistoryStatus = false
	config.Subfolders = nil
	config.DownloadRetryMax = 1
	source := configurationSource{ChannelID: "channel", Destination: filepath.Join(root, "downloads")}
	sourceDefault(&source)
	config.Channels = []configurationSource{source}
	historyJobs.Queue("channel", historyJob{Status: historyStatusRunning, TargetChannelID: "channel"})
	handleHistory(nil, "channel", "", "")
	job, _ := historyJobs.Get("channel")
	if job.Status != historyStatusErrorDownloading {
		t.Errorf("history status after failed download = %v", job.Status)
	}
	if contents, err := os.ReadFile(historyCacheFilePath("channel")); err == nil {
		var cache historyCache
		if err := json.Unmarshal(contents, &cache); err != nil {
			t.Fatal(err)
		}
		if cache.LatestArchived != "" {
			t.Errorf("failed media permanently skipped by checkpoint %s", cache.LatestArchived)
		}
	}
	failed = false
	historyJobs.Queue("channel", historyJob{Status: historyStatusWaiting, TargetChannelID: "channel"})
	if got := handleHistory(nil, "channel", "", ""); got != 1 {
		t.Errorf("retry downloaded %d files, want 1", got)
	}
	contents, err := os.ReadFile(historyCacheFilePath("channel"))
	if err != nil {
		t.Fatal(err)
	}
	var cache historyCache
	if err := json.Unmarshal(contents, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.LatestArchived != "100" || cache.Running {
		t.Errorf("completed retry did not promote checkpoint: %+v", cache)
	}
	expectedBefore = discordTimestampToSnowflake("2006-01-02", "2020-01-01")
	historyJobs.Queue("channel", historyJob{Status: historyStatusWaiting, TargetChannelID: "channel"})
	handleHistory(nil, "channel", "2020-01-01", "")
}
