package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"gopkg.in/ini.v1"
)

func TestIsNumericAcceptsOnlyDecimalDigits(t *testing.T) {
	for _, value := range []string{"0", "123456789012345678"} {
		if !isNumeric(value) {
			t.Errorf("isNumeric(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "1.5", "1e6", "-1", "+1", "NaN", "Inf", " 123"} {
		if isNumeric(value) {
			t.Errorf("isNumeric(%q) = true, want false", value)
		}
	}
}

func TestSafeConfiguredDurationCannotOverflow(t *testing.T) {
	if got := safeConfiguredDuration(5, time.Second); got != 5*time.Second {
		t.Fatalf("safeConfiguredDuration() = %s, want 5s", got)
	}
	if got := safeConfiguredDuration(-1, time.Second); got != 0 {
		t.Fatalf("negative duration = %s, want 0", got)
	}
	if got := safeConfiguredDuration(int(^uint(0)>>1), time.Hour); got <= 0 {
		t.Fatalf("overflowing duration became non-positive: %s", got)
	}
}

func TestRequestShutdownIsNonBlockingAndIdempotent(t *testing.T) {
	oldLoop := loop
	loop = make(chan os.Signal, 1)
	t.Cleanup(func() { loop = oldLoop })
	if !requestShutdown() {
		t.Fatal("first shutdown request was not queued")
	}
	if requestShutdown() {
		t.Fatal("duplicate shutdown request was queued")
	}
	select {
	case signal := <-loop:
		if signal != os.Interrupt {
			t.Fatalf("shutdown signal = %v, want os.Interrupt", signal)
		}
	default:
		t.Fatal("shutdown signal was not available")
	}
}

func TestFormatNumberShortHandlesFullInt64Range(t *testing.T) {
	for _, value := range []int64{1001, -1001, 1<<63 - 1, -1 << 63} {
		if got := formatNumberShort(value); got == "" {
			t.Fatalf("formatNumberShort(%d) returned an empty string", value)
		}
	}
}

func TestClearPaddedSymbolsCanTrimAllSpaces(t *testing.T) {
	if got := clearPaddedSymbols("   "); got != "" {
		t.Fatalf("clearPaddedSymbols() = %q, want empty", got)
	}
}

func TestGetJSONHonorsClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	oldClient := jsonHTTPClient
	jsonHTTPClient = &http.Client{Timeout: 50 * time.Millisecond}
	t.Cleanup(func() { jsonHTTPClient = oldClient })
	var target map[string]interface{}
	started := time.Now()
	if err := getJSON(server.URL, &target); err == nil {
		t.Fatal("getJSON() succeeded despite the server never responding")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("getJSON() timeout took too long: %s", elapsed)
	}
}

func TestRedditParserRejectsEmptyListingsWithoutPanicking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	if _, err := getRedditPostUrls(server.URL); err == nil {
		t.Fatal("getRedditPostUrls() accepted an empty listing")
	}
}

func TestUpdateDiscordPresenceIsSafeBeforeLogin(t *testing.T) {
	oldBot := currentDiscordSession()
	oldReady := botReady.Load()
	oldStatus := config.PresenceStatus
	setDiscordSession(nil)
	botReady.Store(false)
	config.PresenceStatus = string(discordgo.StatusIdle)
	t.Cleanup(func() {
		setDiscordSession(oldBot)
		botReady.Store(oldReady)
		config.PresenceStatus = oldStatus
	})
	updateDiscordPresence()
}

func TestRuntimeTelemetrySupportsConcurrentSnapshots(t *testing.T) {
	oldUpdated, oldDownload, oldMessage := runtimeTelemetrySnapshot()
	t.Cleanup(func() {
		setLastUpdated(oldUpdated)
		setLastDownload(oldDownload)
		setLastMessage(oldMessage)
	})

	var wg sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				value := time.Unix(int64(worker*100+iteration), 0)
				setLastUpdated(value)
				setLastDownload(value)
				setLastMessage(value)
				updated, download, message := runtimeTelemetrySnapshot()
				if updated.IsZero() || download.IsZero() || message.IsZero() {
					t.Errorf("telemetry snapshot unexpectedly contained a zero value")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestAutoHistoryInitiatedSupportsConcurrentAccess(t *testing.T) {
	oldValue := autoHistoryInitiated.Load()
	t.Cleanup(func() { autoHistoryInitiated.Store(oldValue) })
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(value bool) {
			defer wg.Done()
			for iteration := 0; iteration < 100; iteration++ {
				autoHistoryInitiated.Store(value)
				_ = autoHistoryInitiated.Load()
			}
		}(worker%2 == 0)
	}
	wg.Wait()
}

func TestDiscordRuntimePointersUseSynchronizedSnapshots(t *testing.T) {
	oldSession := currentDiscordSession()
	oldUser := currentDiscordUser()
	t.Cleanup(func() {
		setDiscordSession(oldSession)
		setDiscordUser(oldUser)
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	user := &discordgo.User{ID: "123", Username: "tester"}
	setDiscordSession(session)
	setDiscordUser(user)
	if currentDiscordSession() != session || currentDiscordUser() != user {
		t.Fatal("Discord runtime snapshot did not return the installed values")
	}
}

func TestDiscordGatewayConfigurationIsAppliedBeforeOpen(t *testing.T) {
	oldTimeout := config.DiscordTimeout
	config.DiscordTimeout = 47
	t.Cleanup(func() { config.DiscordTimeout = oldTimeout })
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := configureDiscordSessionForGateway(session); err != nil {
		t.Fatal(err)
	}
	if session.Identify.Intents&discordgo.IntentsMessageContent == 0 {
		t.Fatal("Message Content intent was not configured")
	}
	if !session.StateEnabled || !session.State.TrackChannels || !session.State.TrackThreads ||
		!session.State.TrackMembers || !session.State.TrackThreadMembers {
		t.Fatal("Discord state tracking was not fully configured")
	}
	if session.Client.Timeout != 47*time.Second {
		t.Fatalf("Discord HTTP timeout = %s, want 47s", session.Client.Timeout)
	}
}

func TestDiscordHandlersRegisterBeforeGatewayOpen(t *testing.T) {
	oldSession := currentDiscordSession()
	oldCommands := botCommands
	t.Cleanup(func() {
		setDiscordSession(oldSession)
		botCommands = oldCommands
	})
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := registerDiscordHandlers(session); err != nil {
		t.Fatal(err)
	}
	if currentDiscordSession() != session || botCommands == nil || len(botCommands.Routes) == 0 {
		t.Fatal("Discord session handlers and command routes were not installed")
	}
	if err := registerDiscordHandlers(nil); err == nil {
		t.Fatal("nil Discord session was accepted")
	}
}

func TestHasPermsFailsClosedWithoutDiscordSession(t *testing.T) {
	oldSession := currentDiscordSession()
	oldSelfbot := selfbot
	setDiscordSession(nil)
	selfbot = false
	t.Cleanup(func() {
		setDiscordSession(oldSession)
		selfbot = oldSelfbot
	})
	if hasPerms("channel", discordgo.PermissionSendMessages) {
		t.Fatal("hasPerms() allowed access without a Discord session")
	}
}

func TestAuthorizationHelpersFailClosedForIncompleteMessages(t *testing.T) {
	oldConfig := config
	oldUser := currentDiscordUser()
	config = defaultConfiguration()
	config.Admins = []string{"admin-user"}
	config.AllowGeneralCommands = false
	setDiscordUser(nil)
	t.Cleanup(func() {
		config = oldConfig
		setDiscordUser(oldUser)
	})
	if isBotAdmin(nil) {
		t.Fatal("nil message was authorized as a bot admin")
	}
	if isBotAdmin(&discordgo.Message{}) {
		t.Fatal("message without an author was authorized as a bot admin")
	}
	if isCommandableChannel(nil) {
		t.Fatal("nil message was accepted as commandable")
	}
}

func TestBotAdminFailsClosedWhenNoAdminsAreConfigured(t *testing.T) {
	oldConfig := config
	oldUser := currentDiscordUser()
	config = defaultConfiguration()
	config.Admins = nil
	config.AdminChannels = nil
	setDiscordUser(&discordgo.User{ID: "bot-user"})
	t.Cleanup(func() {
		config = oldConfig
		setDiscordUser(oldUser)
	})
	if isBotAdmin(&discordgo.Message{Author: &discordgo.User{ID: "arbitrary-user"}}) {
		t.Fatal("arbitrary user became an administrator when the admin lists were empty")
	}
	if !isBotAdmin(&discordgo.Message{Author: &discordgo.User{ID: "bot-user"}}) {
		t.Fatal("the bot's own identity was not accepted")
	}
}

func TestDataKeysWorksBeforeDiscordUserIsAvailable(t *testing.T) {
	oldConfig := config
	oldUser := currentDiscordUser()
	config = defaultConfiguration()
	setDiscordUser(nil)
	t.Cleanup(func() {
		config = oldConfig
		setDiscordUser(oldUser)
	})
	if got := dataKeys("bot={{botUsername}}"); got != "bot=" {
		t.Fatalf("dataKeys() = %q, want empty pre-login bot username", got)
	}
}

func TestChooseDownloadReactionFallsBackWhenNoEmojiIsEligible(t *testing.T) {
	emojis := []*discordgo.Emoji{
		{ID: "1", Name: "animated", Animated: true},
		{ID: "2", Name: "blocked"},
		nil,
	}
	blacklist := []string{"blocked:2"}
	if got := chooseDownloadReaction(emojis, blacklist, "fallback"); got != "fallback" {
		t.Fatalf("chooseDownloadReaction() = %q, want fallback", got)
	}
}

func TestChooseDownloadReactionUsesSingleEligibleEmoji(t *testing.T) {
	emojis := []*discordgo.Emoji{{ID: "7", Name: "usable"}}
	if got, want := chooseDownloadReaction(emojis, nil, "fallback"), "usable:7"; got != want {
		t.Fatalf("chooseDownloadReaction() = %q, want %q", got, want)
	}
}

func TestDownloadRejectsNonSuccessHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	oldConfig := config
	config = defaultConfiguration()
	t.Cleanup(func() { config = oldConfig })
	request := downloadRequestStruct{
		InputURL:  server.URL,
		Path:      t.TempDir(),
		EmojiCmd:  true,
		FileTime:  time.Now(),
		StartTime: time.Now(),
	}
	status, _ := request.tryDownload()
	if status.Status != downloadFailedCode {
		t.Fatalf("tryDownload() status = %v, want downloadFailedCode", status.Status)
	}
}

func TestDownloadLinkContentAllowlistIsEnforced(t *testing.T) {
	allowed := []string{"cdn.example"}
	blocked := []string{"private"}
	filters := &configurationSourceFilters{AllowedLinkContent: &allowed, BlockedLinkContent: &blocked}
	if !downloadLinkContentAllowed("https://cdn.example/file.png", filters) {
		t.Fatal("matching allowlist link was rejected")
	}
	if downloadLinkContentAllowed("https://other.example/file.png", filters) {
		t.Fatal("link outside allowlist was accepted")
	}
	if downloadLinkContentAllowed("https://cdn.example/private/file.png", filters) {
		t.Fatal("blocklist must take precedence over allowlist")
	}
}

func TestHandleCommandsRegistersRoutesSynchronously(t *testing.T) {
	oldBot := bot
	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatal(err)
	}
	bot = session
	t.Cleanup(func() { bot = oldBot })
	router := handleCommands()
	if got := len(router.Routes); got < 6 {
		t.Fatalf("handleCommands() returned with only %d routes", got)
	}
}

func TestHistoryReactionOverrideCanDisableGlobalSetting(t *testing.T) {
	oldRealtime := config.ReactWhenDownloaded
	oldHistory := config.ReactWhenDownloadedHistory
	config.ReactWhenDownloaded = true
	config.ReactWhenDownloadedHistory = true
	t.Cleanup(func() {
		config.ReactWhenDownloaded = oldRealtime
		config.ReactWhenDownloadedHistory = oldHistory
	})

	disabled := false
	source := configurationSource{ReactWhenDownloadedHistory: &disabled}
	if shouldReactToDownload(true, source) {
		t.Fatal("per-source history reaction override=false must disable reactions")
	}
	if !shouldReactToDownload(false, source) {
		t.Fatal("history-only override must not disable real-time reactions")
	}
}

func TestSetHistoryJobStatusAndTerminalPriority(t *testing.T) {
	oldJobs := historyJobs
	historyJobs = newHistoryJobStore()
	historyJobs.Set("channel", historyJob{Status: historyStatusWaiting})
	t.Cleanup(func() { historyJobs = oldJobs })

	if !setHistoryJobStatus("channel", historyStatusErrorReadMessageHistoryPerms) {
		t.Fatal("setHistoryJobStatus() did not find existing job")
	}
	job, _ := historyJobs.Get("channel")
	if job.Status != historyStatusErrorReadMessageHistoryPerms || job.Updated.IsZero() {
		t.Fatalf("job status/update not persisted: %+v", job)
	}

	status := mergeHistoryTerminalStatus(historyStatusCompletedNoMoreMessages, historyStatusErrorRequesting)
	status = mergeHistoryTerminalStatus(status, historyStatusCompletedToSinceFilter)
	if status != historyStatusErrorRequesting {
		t.Fatalf("completion overwrote an error status: %v", status)
	}
}

func TestHistoryJobStoreCancellationCannotBeLostByStart(t *testing.T) {
	store := newHistoryJobStore()
	store.Set("channel", historyJob{Status: historyStatusWaiting, TargetChannelID: "channel"})
	found, changed := store.RequestCancel("channel")
	if !found || !changed {
		t.Fatal("waiting history job was not cancelled")
	}
	if _, started := store.StartWaiting("channel"); started {
		t.Fatal("cancelled waiting job was started from a stale manager snapshot")
	}
	job, _ := store.Get("channel")
	if job.Status != historyStatusAbortCompleted {
		t.Fatalf("cancelled job status = %v, want abort completed", job.Status)
	}
}

func TestHistoryJobStoreQueueResetsProgressAndRejectsActiveReplacement(t *testing.T) {
	store := newHistoryJobStore()
	first := historyJob{Status: historyStatusWaiting, TargetChannelID: "channel", DownloadCount: 99, DownloadSize: 99}
	if !store.Queue("channel", first) {
		t.Fatal("first history job was not queued")
	}
	job, _ := store.Get("channel")
	if job.DownloadCount != 0 || job.DownloadSize != 0 {
		t.Fatalf("new job retained stale progress: %+v", job)
	}
	if store.Queue("channel", historyJob{Status: historyStatusWaiting, TargetChannelID: "replacement"}) {
		t.Fatal("active waiting job was replaced")
	}
	if !store.SetProgress("channel", 4, 1024) {
		t.Fatal("failed to update history progress")
	}
	job, _ = store.Get("channel")
	if job.DownloadCount != 4 || job.DownloadSize != 1024 {
		t.Fatalf("history progress = %d/%d, want 4/1024", job.DownloadCount, job.DownloadSize)
	}
}

func TestHistoryHelpersIgnoreNilMessagesAndAbortDelay(t *testing.T) {
	messages := compactHistoryMessages([]*discordgo.Message{
		nil,
		&discordgo.Message{ID: "1"},
		nil,
		&discordgo.Message{ID: "2"},
	})
	if len(messages) != 2 || messages[0].ID != "1" || messages[1].ID != "2" {
		t.Fatalf("compactHistoryMessages() = %#v", messages)
	}

	oldJobs := historyJobs
	historyJobs = newHistoryJobStore()
	historyJobs.Set("channel", historyJob{Status: historyStatusAbortRequested})
	t.Cleanup(func() { historyJobs = oldJobs })
	started := time.Now()
	if !waitForHistoryRequestDelay("channel", time.Minute) {
		t.Fatal("waitForHistoryRequestDelay() did not observe abort request")
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("abort took too long: %s", elapsed)
	}
}

func TestHistoryCachePathCannotEscapeCacheDirectory(t *testing.T) {
	oldPath := pathCacheHistory
	pathCacheHistory = t.TempDir()
	t.Cleanup(func() { pathCacheHistory = oldPath })
	got := historyCacheFilePath("..")
	if !pathIsWithinBase(pathCacheHistory, got) {
		t.Fatalf("historyCacheFilePath() escaped cache directory: %q", got)
	}
}

func TestCollectArchivedThreadsPaginates(t *testing.T) {
	newer := time.Now()
	older := newer.Add(-time.Hour)
	calls := 0
	threads, err := collectArchivedThreads(func(before *time.Time) (*discordgo.ThreadsList, error) {
		calls++
		if calls == 1 {
			if before != nil {
				t.Fatal("first page unexpectedly received a cursor")
			}
			return &discordgo.ThreadsList{HasMore: true, Threads: []*discordgo.Channel{{
				ID: "newer", ThreadMetadata: &discordgo.ThreadMetadata{ArchiveTimestamp: newer},
			}, {
				ID: "older", ThreadMetadata: &discordgo.ThreadMetadata{ArchiveTimestamp: older},
			}}}, nil
		}
		if before == nil || !before.Equal(older) {
			t.Fatalf("second page cursor = %v, want %v", before, older)
		}
		return &discordgo.ThreadsList{Threads: []*discordgo.Channel{{ID: "oldest"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(threads) != 3 {
		t.Fatalf("collectArchivedThreads() made %d calls and returned %d threads", calls, len(threads))
	}
}

func TestGetSourceExactChannelWorksWithoutDiscordState(t *testing.T) {
	oldConfig := config
	oldBot := currentDiscordSession()
	configured := true
	config = defaultConfiguration()
	config.Channels = []configurationSource{{ChannelID: "123", Enabled: &configured}}
	setDiscordSession(nil)
	t.Cleanup(func() {
		config = oldConfig
		setDiscordSession(oldBot)
	})

	if got := getSource(&discordgo.Message{ChannelID: "123"}); got.ChannelID != "123" {
		t.Fatalf("getSource() did not return exact configured channel: %+v", got)
	}
	if got := getSource(&discordgo.Message{ChannelID: "missing"}); got.ChannelID != "" {
		t.Fatalf("getSource() returned a source for an unknown channel: %+v", got)
	}
}

func TestBoundAndRegisteredChannelsIncludeSingleAndMultiIDs(t *testing.T) {
	oldConfig := config
	multiple := []string{"200", "300"}
	config = defaultConfiguration()
	config.Channels = []configurationSource{{ChannelID: "100", ChannelIDs: &multiple}}
	t.Cleanup(func() { config = oldConfig })

	if got := getBoundChannelsCount(); got != 3 {
		t.Fatalf("getBoundChannelsCount() = %d, want 3", got)
	}
	registered := getAllRegisteredChannels()
	if len(registered) != 3 {
		t.Fatalf("getAllRegisteredChannels() returned %d channels, want 3", len(registered))
	}
}

func TestConfigurationDecoderPreservesValidJSONEscapes(t *testing.T) {
	decoded, used, err := decodeConfigurationWithLegacyFallback(`{"logOutput":"line\nbreak"}`, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LogOutput != "line\nbreak" {
		t.Fatalf("LogOutput = %q, want an embedded newline", decoded.LogOutput)
	}
	if used != `{"logOutput":"line\nbreak"}` {
		t.Fatalf("valid JSON unexpectedly entered legacy fallback: %q", used)
	}
}

func TestConfigurationDecoderSupportsLegacyWindowsPaths(t *testing.T) {
	decoded, _, err := decodeConfigurationWithLegacyFallback(`{"logOutput":"C:\logs\bot.log"}`, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LogOutput != `C:\logs\bot.log` {
		t.Fatalf("LogOutput = %q, want legacy Windows path", decoded.LogOutput)
	}
}

func TestConfigurationLegacyFallbackPreservesOtherValidEscapes(t *testing.T) {
	decoded, _, err := decodeConfigurationWithLegacyFallback(
		`{"logOutput":"C:\logs\bot.log","presenceStatus":"line\nbreak"}`, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LogOutput != `C:\logs\bot.log` {
		t.Fatalf("LogOutput = %q, want legacy Windows path", decoded.LogOutput)
	}
	if decoded.PresenceStatus != "line\nbreak" {
		t.Fatalf("PresenceStatus = %q, want embedded newline preserved", decoded.PresenceStatus)
	}
}

func TestLegacyINIImportWritesTypedTargets(t *testing.T) {
	cfg, err := ini.Load([]byte("[auth]\ntoken = imported-token\n[general]\ndownload timeout = 42\n[status]\nstatus enabled = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	token := "old"
	timeout := 0
	enabled := true
	if !importLegacyINIKey(cfg, "auth", "token", &token) || token != "imported-token" {
		t.Fatalf("token import = %q, want imported-token", token)
	}
	if !importLegacyINIKey(cfg, "general", "download timeout", &timeout) || timeout != 42 {
		t.Fatalf("timeout import = %d, want 42", timeout)
	}
	if !importLegacyINIKey(cfg, "status", "status enabled", &enabled) || enabled {
		t.Fatalf("status enabled import = %v, want false", enabled)
	}
	if importLegacyINIKey(cfg, "missing", "key", &token) {
		t.Fatal("missing INI key was reported as imported")
	}
}

func TestDownloadSubfolderFallbackUsesFallbackTemplates(t *testing.T) {
	primary := []string{"{{unknownTemplate}}"}
	fallback := []string{"fallback", "{{fileType}}"}
	source := configurationSource{Subfolders: &primary, SubfoldersFallback: &fallback}
	sourceDefault(&source)
	got := formattedDownloadSubfolders(source, downloadRequestStruct{Message: &discordgo.Message{}}, "image")
	if len(got) != 2 || got[0] != "fallback" || got[1] != "images" {
		t.Fatalf("formattedDownloadSubfolders() = %#v, want [fallback images]", got)
	}
}

func TestGUIConfigurationReaderSupportsLegacyWindowsPaths(t *testing.T) {
	oldFile := configFile
	configFile = filepath.Join(t.TempDir(), "legacy.json")
	t.Cleanup(func() { configFile = oldFile })
	if err := os.WriteFile(configFile, []byte(`{"logOutput":"C:\logs\bot.log"}`), 0600); err != nil {
		t.Fatal(err)
	}
	decoded, err := guiReadConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if decoded.LogOutput != `C:\logs\bot.log` {
		t.Fatalf("LogOutput = %q, want legacy Windows path", decoded.LogOutput)
	}
}

func TestNormalizeLoadedConfigurationRestoresRequiredPointers(t *testing.T) {
	candidate := defaultConfiguration()
	candidate.Credentials.TwitterAuthEnabled = nil
	candidate.Credentials.InstagramAuthEnabled = nil
	candidate.InflateDownloadCount = nil
	candidate.Filters = nil
	normalizeLoadedConfiguration(&candidate)
	if candidate.Credentials.TwitterAuthEnabled == nil || candidate.Credentials.InstagramAuthEnabled == nil ||
		candidate.InflateDownloadCount == nil || candidate.Filters == nil {
		t.Fatalf("required configuration pointers remain nil: %+v", candidate)
	}
}

func TestConfigurationOutputRedactsEveryCredentialSecret(t *testing.T) {
	candidate := defaultConfiguration()
	totp := "instagram-totp-secret"
	candidate.Credentials.Token = "discord-secret"
	candidate.Credentials.Password = "discord-password"
	candidate.Credentials.TwitterPassword = "twitter-password"
	candidate.Credentials.Twitter2FA = "twitter-2fa"
	candidate.Credentials.TwitterAuthToken = "twitter-auth-cookie"
	candidate.Credentials.TwitterCT0 = "twitter-csrf-cookie"
	candidate.Credentials.TwitterProxy = "http://user:pass@proxy.test"
	candidate.Credentials.InstagramPassword = "instagram-password"
	candidate.Credentials.InstagramTOTP = &totp
	candidate.Credentials.InstagramProxy = "http://instagram:pass@proxy.test"
	candidate.Credentials.FlickrApiKey = "flickr-secret"
	redactConfigurationForOutput(&candidate)
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"discord-secret", "discord-password", "twitter-password", "twitter-2fa",
		"twitter-auth-cookie", "twitter-csrf-cookie", "user:pass", "instagram-password",
		"instagram-totp-secret", "instagram:pass", "flickr-secret",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("redacted configuration still contains %q", secret)
		}
	}
}

func TestOverwriteCachePathMovesAllDerivedCacheFiles(t *testing.T) {
	oldCache, oldHistory := pathCache, pathCacheHistory
	oldJSON, oldYAML := pathCacheSettingsJSON, pathCacheSettingsYAML
	oldDuplo, oldTwitter := pathCacheDuplo, pathCacheTwitter
	oldInstagram, oldConstants := pathCacheInstagram, pathConstants
	oldDatabase, oldBackups := pathDatabaseBase, pathDatabaseBackups
	t.Cleanup(func() {
		pathCache, pathCacheHistory = oldCache, oldHistory
		pathCacheSettingsJSON, pathCacheSettingsYAML = oldJSON, oldYAML
		pathCacheDuplo, pathCacheTwitter = oldDuplo, oldTwitter
		pathCacheInstagram, pathConstants = oldInstagram, oldConstants
		pathDatabaseBase, pathDatabaseBackups = oldDatabase, oldBackups
	})

	candidate := defaultConfiguration()
	candidate.OverwriteCachePath = filepath.Join("custom", "cache")
	applyConfigurationPaths(candidate)
	for name, got := range map[string]string{
		"history": pathCacheHistory, "json": pathCacheSettingsJSON, "yaml": pathCacheSettingsYAML,
		"duplo": pathCacheDuplo, "twitter": pathCacheTwitter, "instagram": pathCacheInstagram,
		"constants": pathConstants,
	} {
		if !strings.HasPrefix(filepath.Clean(got), filepath.Clean(candidate.OverwriteCachePath)+string(os.PathSeparator)) {
			t.Errorf("%s path %q did not move under cache base %q", name, got, candidate.OverwriteCachePath)
		}
	}
}

func TestConfigurationReloadRestoresBuiltInReaction(t *testing.T) {
	oldReaction := defaultReact
	t.Cleanup(func() { defaultReact = oldReaction })
	override := "custom"
	candidate := defaultConfiguration()
	candidate.OverwriteDefaultReaction = &override
	applyConfigurationReaction(candidate)
	if defaultReact != override {
		t.Fatalf("default reaction = %q, want override %q", defaultReact, override)
	}
	candidate.OverwriteDefaultReaction = nil
	applyConfigurationReaction(candidate)
	if defaultReact != builtInDefaultReact {
		t.Fatalf("default reaction after reload = %q, want %q", defaultReact, builtInDefaultReact)
	}
}
