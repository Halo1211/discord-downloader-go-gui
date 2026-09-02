package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGUIStartAndServeLocally(t *testing.T) {
	oldAddress := config.GUIAddress
	oldOpen := config.GUIOpenBrowser
	oldFlagAddress := guiFlagAddress
	oldFlagOpen := guiFlagOpen
	oldFlagOpenSet := guiFlagOpenWasSet
	config.GUIAddress = "127.0.0.1:0"
	config.GUIOpenBrowser = false
	guiFlagAddress = ""
	guiFlagOpen = false
	guiFlagOpenWasSet = true
	t.Cleanup(func() {
		shutdownGUI()
		config.GUIAddress = oldAddress
		config.GUIOpenBrowser = oldOpen
		guiFlagAddress = oldFlagAddress
		guiFlagOpen = oldFlagOpen
		guiFlagOpenWasSet = oldFlagOpenSet
	})

	if err := startGUI(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(guiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Discord Archive Console") {
		t.Fatalf("unexpected GUI response %d: %s", response.StatusCode, string(body))
	}
}

func TestParseRuntimeArgsGUIAndLegacyConfig(t *testing.T) {
	oldBase := configFileBase
	oldEnabled := guiFlagEnabled
	oldDisabled := guiFlagDisabled
	oldAddress := guiFlagAddress
	oldOpen := guiFlagOpen
	oldOpenSet := guiFlagOpenWasSet
	oldAutoStart := guiFlagAutoStart
	oldNoTray := guiFlagNoTray
	oldMinimized := guiFlagMinimized
	t.Cleanup(func() {
		configFileBase = oldBase
		guiFlagEnabled = oldEnabled
		guiFlagDisabled = oldDisabled
		guiFlagAddress = oldAddress
		guiFlagOpen = oldOpen
		guiFlagOpenWasSet = oldOpenSet
		guiFlagAutoStart = oldAutoStart
		guiFlagNoTray = oldNoTray
		guiFlagMinimized = oldMinimized
	})

	parseRuntimeArgs([]string{"custom-settings", "--gui", "--gui-address=127.0.0.1:8899", "--no-gui-open", "--gui-auto-start", "--no-tray", "--start-minimized"})
	if configFileBase != "custom-settings" || !guiFlagEnabled {
		t.Fatalf("unexpected parsed state: base=%q gui=%v", configFileBase, guiFlagEnabled)
	}
	if guiFlagAddress != "127.0.0.1:8899" || guiFlagOpen || !guiFlagOpenWasSet {
		t.Fatalf("unexpected GUI options: address=%q open=%v set=%v", guiFlagAddress, guiFlagOpen, guiFlagOpenWasSet)
	}
	if !guiFlagAutoStart || !guiFlagNoTray || !guiFlagMinimized {
		t.Fatal("tray runtime options were not parsed")
	}
}

func TestNoGUIFlagOverridesAutomaticGUI(t *testing.T) {
	oldEnabled := config.GUIEnabled
	oldFlagEnabled := guiFlagEnabled
	oldFlagDisabled := guiFlagDisabled
	config.GUIEnabled = true
	guiFlagEnabled = false
	guiFlagDisabled = false
	t.Cleanup(func() {
		config.GUIEnabled = oldEnabled
		guiFlagEnabled = oldFlagEnabled
		guiFlagDisabled = oldFlagDisabled
	})

	if !shouldStartGUI() {
		t.Fatal("GUI should start automatically by default")
	}
	parseRuntimeArgs([]string{"--no-gui"})
	if shouldStartGUI() {
		t.Fatal("--no-gui should disable automatic GUI startup")
	}
}

func TestGUIPageHasSecurityHeadersAndToken(t *testing.T) {
	oldToken := guiToken
	guiToken = "test-token"
	t.Cleanup(func() { guiToken = oldToken })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "127.0.0.1:7744"
	guiSecurityHeaders(http.HandlerFunc(guiPageHandler)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "test-token") {
		t.Fatal("GUI token was not embedded in the page")
	}
	if recorder.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing clickjacking protection")
	}
}

func TestGUIRejectsNonLocalListenerAndRebindingHost(t *testing.T) {
	oldToken := guiToken
	guiToken = "rebind-secret-token"
	t.Cleanup(func() { guiToken = oldToken })
	for _, address := range []string{"0.0.0.0:7744", ":7744", "192.0.2.10:7744", "public.example:7744"} {
		if err := validateLocalGUIAddress(address); err == nil {
			t.Errorf("validateLocalGUIAddress(%q) accepted a non-local address", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "localhost:7744", "[::1]:7744"} {
		if err := validateLocalGUIAddress(address); err != nil {
			t.Errorf("validateLocalGUIAddress(%q) rejected a local address: %v", address, err)
		}
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Host = "attacker.example:7744"
	guiSecurityHeaders(http.HandlerFunc(guiPageHandler)).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || strings.Contains(recorder.Body.String(), guiToken) {
		t.Fatalf("rebinding-style Host was not safely rejected: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGUIDecodeJSONRejectsTrailingValues(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"channelId":"123"} {"channelId":"456"}`))
	var target guiHistoryRequest
	if err := guiDecodeJSON(request, &target); err == nil {
		t.Fatal("guiDecodeJSON accepted multiple JSON values")
	}
}

func TestGUIDecodeJSONRejectsOversizedBody(t *testing.T) {
	body := `{"channelId":"123"}` + strings.Repeat(" ", 16*1024)
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var target guiHistoryRequest
	if err := guiDecodeJSON(request, &target); err == nil {
		t.Fatal("guiDecodeJSON accepted a request body larger than its limit")
	}
}

func TestGUIValidationRejectsResourceExhaustionSettings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*configuration)
	}{
		{name: "download retries", mutate: func(candidate *configuration) { candidate.DownloadRetryMax = maxConfigDownloadRetry + 1 }},
		{name: "download workers", mutate: func(candidate *configuration) { candidate.DownloadWorkers = 129; candidate.DownloadQueueSize = 129 }},
		{name: "download queue", mutate: func(candidate *configuration) { candidate.DownloadQueueSize = 1_000_001 }},
		{name: "history jobs", mutate: func(candidate *configuration) { candidate.HistoryMaxJobs = 65 }},
		{name: "history workers", mutate: func(candidate *configuration) { candidate.HistoryDownloadWorkers = 129 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := defaultConfiguration()
			test.mutate(&candidate)
			if err := guiValidateConfiguration(candidate); err == nil {
				t.Fatal("guiValidateConfiguration accepted an unsafe resource limit")
			}
		})
	}
}

func TestGUIPageIncludesInteractiveRelayControls(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	guiPageHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", recorder.Code)
	}

	page := recorder.Body.String()
	required := []string{
		`id="runtimeStartButton"`,
		`function startRuntime`,
		`/api/runtime/start`,
		`id="jobsSearch"`,
		`data-job-filter="active"`,
		`id="liveToggle"`,
		`id="copySnapshot"`,
		`id="languageSelect"`,
		`id="themeSelect"`,
		`id="settingsSave"`,
		`id="settingsNav"`,
		`id="settingsImport"`,
		`id="settingsExport"`,
		`id="settingsDiscard"`,
		`id="consoleOutput"`,
		`id="consoleFollow"`,
		`id="consoleCopy"`,
		`id="consoleClear"`,
		`function refreshConsole`,
		`id="settingsModeNormal"`,
		`id="settingsModeAdvanced"`,
		`function setSettingsMode`,
		`function consoleLineKind`,
		`byId('liveToggle').addEventListener`,
		`byId('copySnapshot').addEventListener`,
		`byId('languageSelect').addEventListener`,
		`byId('themeSelect').addEventListener`,
		`Configuration center`,
		`sourceSetupGuide`,
		`function addServerGroup`,
		`Server → channel → penyimpanan`,
		`Otomatis: server/channel`,
		`function automaticChannelSubfolders`,
		`{{channelName}}`,
		`byId('settingsImport').addEventListener`,
		`byId('settingsExport').addEventListener`,
		`byId('settingsDiscard').addEventListener`,
	}
	for _, marker := range required {
		if !strings.Contains(page, marker) {
			t.Errorf("interactive GUI marker %q is missing", marker)
		}
	}
}

func TestGUIPageHasNoDuplicateElementIDs(t *testing.T) {
	matches := regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(guiHTML, -1)
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		if seen[match[1]] {
			t.Fatalf("duplicate GUI element id %q", match[1])
		}
		seen[match[1]] = true
	}
}

func TestGUIStatusWorksBeforeDiscordReady(t *testing.T) {
	oldManager := currentDownloadManager()
	oldRuntimeState := getGUIRuntimeState()
	setDownloadManager(nil)
	setGUIRuntimeState("waiting")
	t.Cleanup(func() {
		setDownloadManager(oldManager)
		setGUIRuntimeState(oldRuntimeState)
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	guiStatusHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"ready":false`) {
		t.Fatalf("expected not-ready status: %s", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"runtime":{"state":"waiting"}`) {
		t.Fatalf("expected waiting runtime status: %s", recorder.Body.String())
	}
}

func TestGUIRuntimeStartRequiresTokenAndSignalsWaitingRuntime(t *testing.T) {
	oldToken := guiToken
	oldConfigFile := configFile
	oldRuntimeState := getGUIRuntimeState()
	oldStartupIssue := getGUIStartupIssue()
	guiToken = "start-token"
	configFile = filepath.Join(t.TempDir(), "settings.json")
	candidate := defaultConfiguration()
	candidate.Credentials.Token = placeholderToken
	writeCandidate := func() {
		content, err := json.Marshal(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configFile, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCandidate()
	select {
	case <-guiRuntimeStart:
	default:
	}
	setGUIRuntimeState("waiting")
	t.Cleanup(func() {
		select {
		case <-guiRuntimeStart:
		default:
		}
		guiToken = oldToken
		configFile = oldConfigFile
		setGUIRuntimeState(oldRuntimeState)
		setGUIStartupIssue(oldStartupIssue)
	})

	unauthorized := httptest.NewRecorder()
	guiStartRuntimeHandler(unauthorized, httptest.NewRequest(http.MethodPost, "/api/runtime/start", nil))
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("unexpected unauthorized status %d", unauthorized.Code)
	}

	missingToken := httptest.NewRecorder()
	missingTokenRequest := httptest.NewRequest(http.MethodPost, "/api/runtime/start", nil)
	missingTokenRequest.Header.Set("X-DDG-GUI-Token", guiToken)
	guiStartRuntimeHandler(missingToken, missingTokenRequest)
	if missingToken.Code != http.StatusUnprocessableEntity || getGUIRuntimeState() != "waiting" {
		t.Fatalf("missing token start was not rejected safely: %d %s", missingToken.Code, missingToken.Body.String())
	}

	candidate.Credentials.Token = "valid-test-token"
	writeCandidate()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/runtime/start", nil)
	request.Header.Set("X-DDG-GUI-Token", guiToken)
	guiStartRuntimeHandler(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("unexpected start status %d: %s", recorder.Code, recorder.Body.String())
	}
	if state := getGUIRuntimeState(); state != "starting" {
		t.Fatalf("runtime state = %q, want starting", state)
	}
	select {
	case <-guiRuntimeStart:
	default:
		t.Fatal("runtime start signal was not delivered")
	}

	duplicate := httptest.NewRecorder()
	duplicateRequest := httptest.NewRequest(http.MethodPost, "/api/runtime/start", nil)
	duplicateRequest.Header.Set("X-DDG-GUI-Token", guiToken)
	guiStartRuntimeHandler(duplicate, duplicateRequest)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("unexpected duplicate start status %d", duplicate.Code)
	}
}

func TestGUIStatusReportsStartupIssue(t *testing.T) {
	oldIssue := getGUIStartupIssue()
	oldManager := currentDownloadManager()
	setGUIStartupIssue("settings file needs a Discord token")
	setDownloadManager(nil)
	t.Cleanup(func() {
		setGUIStartupIssue(oldIssue)
		setDownloadManager(oldManager)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	guiStatusHandler(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"startupIssue":"settings file needs a Discord token"`) {
		t.Fatalf("expected startup issue in status: %s", recorder.Body.String())
	}
}

func TestGUIStatusReportsHistoryProgress(t *testing.T) {
	oldJobs := historyJobs
	historyJobs = newHistoryJobStore()
	historyJobs.Set("123456789012345678", historyJob{
		Status:        historyStatusRunning,
		OriginUser:    "tester",
		DownloadCount: 7,
		DownloadSize:  2048,
		Added:         time.Now(),
		Updated:       time.Now(),
	})
	t.Cleanup(func() { historyJobs = oldJobs })

	recorder := httptest.NewRecorder()
	guiStatusHandler(recorder, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status endpoint returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		History struct {
			Jobs []struct {
				DownloadCount int64 `json:"downloadCount"`
				DownloadSize  int64 `json:"downloadSize"`
			} `json:"jobs"`
		} `json:"history"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.History.Jobs) != 1 {
		t.Fatalf("status endpoint returned %d history jobs, want 1", len(response.History.Jobs))
	}
	if got := response.History.Jobs[0]; got.DownloadCount != 7 || got.DownloadSize != 2048 {
		t.Fatalf("history progress = %+v, want 7 downloads and 2048 bytes", got)
	}
}

func TestGUICancelWaitingHistoryJob(t *testing.T) {
	oldStore := historyJobs
	oldToken := guiToken
	historyJobs = newHistoryJobStore()
	guiToken = "cancel-token"
	t.Cleanup(func() {
		historyJobs = oldStore
		guiToken = oldToken
	})
	historyJobs.Set("123456789012345678", historyJob{
		Status:  historyStatusWaiting,
		Added:   time.Now(),
		Updated: time.Now(),
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/history/cancel", strings.NewReader(`{"channelId":"123456789012345678"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-DDG-GUI-Token", "cancel-token")
	guiCancelHistoryHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}
	job, exists := historyJobs.Get("123456789012345678")
	if !exists || job.Status != historyStatusAbortCompleted {
		t.Fatalf("waiting job was not cancelled: %+v", job)
	}
}

func TestGUIRejectsUnauthorizedMutation(t *testing.T) {
	oldToken := guiToken
	guiToken = "required-token"
	t.Cleanup(func() { guiToken = oldToken })

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/history/cancel", strings.NewReader(`{"channelId":"1"}`))
	guiCancelHistoryHandler(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unexpected status %d", recorder.Code)
	}
}

func TestGUIAuthorizationFailsClosedBeforeTokenInitialization(t *testing.T) {
	oldToken := guiToken
	guiToken = ""
	t.Cleanup(func() { guiToken = oldToken })
	request := httptest.NewRequest(http.MethodPost, "/api/runtime/start", nil)
	if guiAuthorized(request) {
		t.Fatal("empty uninitialized GUI token authorized a request")
	}
	if guiAuthorized(nil) {
		t.Fatal("nil GUI request was authorized")
	}
}

func TestGUIConfigEditorIncludesAllFieldsAndSavesBackup(t *testing.T) {
	oldFile := configFile
	oldToken := guiToken
	oldIssue := getGUIStartupIssue()
	oldRuntimeState := getGUIRuntimeState()
	configFile = filepath.Join(t.TempDir(), "settings.json")
	guiToken = "settings-token"
	setGUIRuntimeState("waiting")
	t.Cleanup(func() {
		configFile = oldFile
		guiToken = oldToken
		setGUIStartupIssue(oldIssue)
		setGUIRuntimeState(oldRuntimeState)
	})

	candidate := defaultConfiguration()
	candidate.Credentials.Token = "top-secret-token"
	candidate.Credentials.Email = "private@example.com"
	initial, err := json.MarshalIndent(candidate, "", "\t")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, initial, 0600); err != nil {
		t.Fatal(err)
	}

	getRecorder := httptest.NewRecorder()
	getRequest := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	getRequest.Header.Set("X-DDG-GUI-Token", guiToken)
	guiConfigHandler(getRecorder, getRequest)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected GET status %d: %s", getRecorder.Code, getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `"overwriteCachePath":""`) || !strings.Contains(getRecorder.Body.String(), `"all":null`) {
		t.Fatalf("complete editor fields are missing: %s", getRecorder.Body.String())
	}
	if !strings.Contains(getRecorder.Body.String(), `"prefix":""`) || !strings.Contains(getRecorder.Body.String(), `"content":""`) {
		t.Fatalf("optional nested templates are missing: %s", getRecorder.Body.String())
	}
	if strings.Contains(getRecorder.Body.String(), "top-secret-token") || strings.Contains(getRecorder.Body.String(), "private@example.com") || !strings.Contains(getRecorder.Body.String(), `"credentials.token":true`) || !strings.Contains(getRecorder.Body.String(), `"credentials.email":true`) {
		t.Fatalf("configured credentials were not safely redacted: %s", getRecorder.Body.String())
	}

	candidate.CommandPrefix = "archive "
	candidate.Credentials.Token = ""
	payload, err := json.Marshal(map[string]interface{}{"config": candidate})
	if err != nil {
		t.Fatal(err)
	}
	postRecorder := httptest.NewRecorder()
	postRequest := httptest.NewRequest(http.MethodPost, "/api/config", bytes.NewReader(payload))
	postRequest.Header.Set("Content-Type", "application/json")
	postRequest.Header.Set("X-DDG-GUI-Token", guiToken)
	guiConfigHandler(postRecorder, postRequest)
	if postRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected POST status %d: %s", postRecorder.Code, postRecorder.Body.String())
	}
	if !strings.Contains(postRecorder.Body.String(), `"startAppliesRuntime":true`) || !strings.Contains(getGUIStartupIssue(), "Start loads runtime settings") {
		t.Fatalf("waiting runtime should apply saved settings on Start: response=%s issue=%q", postRecorder.Body.String(), getGUIStartupIssue())
	}
	if _, err := os.Stat(configFile + ".bak"); err != nil {
		t.Fatalf("settings backup was not created: %v", err)
	}
	saved, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	var savedConfig configuration
	if err := json.Unmarshal(saved, &savedConfig); err != nil {
		t.Fatal(err)
	}
	if savedConfig.CommandPrefix != "archive " {
		t.Fatalf("setting was not saved: %q", savedConfig.CommandPrefix)
	}
	if savedConfig.Credentials.Token != "top-secret-token" {
		t.Fatal("blank GUI token field did not preserve the configured token")
	}
	if savedConfig.Credentials.Email != "private@example.com" {
		t.Fatal("blank GUI email field did not preserve the configured email")
	}
}

func TestGUIAtomicWritePreservesBackupWhenMainFileIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	backup := path + ".bak"
	if err := os.WriteFile(backup, []byte("recovery-copy"), 0600); err != nil {
		t.Fatal(err)
	}

	backupCreated, err := guiAtomicWriteConfiguration(path, []byte("new-settings"))
	if err != nil {
		t.Fatal(err)
	}
	if backupCreated != "" {
		t.Fatalf("guiAtomicWriteConfiguration reported a new backup %q without an original file", backupCreated)
	}
	recovery, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("existing recovery backup was removed: %v", err)
	}
	if string(recovery) != "recovery-copy" {
		t.Fatalf("existing recovery backup changed to %q", recovery)
	}
}

func TestGUIAtomicWriteSafelyRotatesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	backup := path + ".bak"
	if err := os.WriteFile(path, []byte("current-settings"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup, []byte("older-backup"), 0600); err != nil {
		t.Fatal(err)
	}

	backupCreated, err := guiAtomicWriteConfiguration(path, []byte("new-settings"))
	if err != nil {
		t.Fatal(err)
	}
	if backupCreated != backup {
		t.Fatalf("backup path = %q, want %q", backupCreated, backup)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "new-settings" || string(previous) != "current-settings" {
		t.Fatalf("atomic rotation produced current=%q backup=%q", current, previous)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".settings.json.bak.*.old"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("staged backups were not cleaned up: %v", leftovers)
	}
}
