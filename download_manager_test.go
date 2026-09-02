package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HouzuoGuo/tiedot/db"
	"github.com/bwmarrin/discordgo"
)

func TestResponseRetryAfterSeconds(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{"1.5"}}}
	if got := responseRetryAfter(response); got != 1500*time.Millisecond {
		t.Fatalf("responseRetryAfter() = %s, want 1.5s", got)
	}
}

func TestRetryAfterAndDownloadBackoffAreBounded(t *testing.T) {
	response := &http.Response{Header: http.Header{"Retry-After": []string{"999999999999"}}}
	if got := responseRetryAfter(response); got != 5*time.Minute {
		t.Fatalf("responseRetryAfter() = %s, want 5m", got)
	}
	oldTimeout := config.DownloadTimeout
	config.DownloadTimeout = 7
	t.Cleanup(func() { config.DownloadTimeout = oldTimeout })
	if got := boundedDownloadRetryDelay(time.Hour); got != 7*time.Second {
		t.Fatalf("boundedDownloadRetryDelay() = %s, want 7s", got)
	}
}

func TestEffectiveDownloadRetryAttemptsAreBounded(t *testing.T) {
	for _, test := range []struct {
		configured int
		want       int
	}{
		{configured: -1, want: 1},
		{configured: 1, want: 1},
		{configured: 5, want: 5},
		{configured: 1_000_000, want: maxConfigDownloadRetry},
	} {
		if got := effectiveDownloadRetryAttempts(test.configured); got != test.want {
			t.Errorf("effectiveDownloadRetryAttempts(%d) = %d, want %d", test.configured, got, test.want)
		}
	}
}

func TestValidDownloadURLRequiresHTTPHost(t *testing.T) {
	for _, input := range []string{"file:///C:/secret.txt", "/relative/path", "javascript:alert(1)", "https:///missing-host"} {
		if _, err := validDownloadURL(input); err == nil {
			t.Errorf("validDownloadURL(%q) unexpectedly succeeded", input)
		}
	}
	for _, input := range []string{"https://example.com/file.png", "http://127.0.0.1:8080/file", "HTTPS://EXAMPLE.COM/%E6%97%A5%E6%9C%AC%E8%AA%9E.png"} {
		parsed, err := validDownloadURL(input)
		if err != nil {
			t.Errorf("validDownloadURL(%q) failed: %v", input, err)
			continue
		}
		if parsed.Scheme != strings.ToLower(parsed.Scheme) {
			t.Errorf("validDownloadURL(%q) did not canonicalize the scheme: %q", input, parsed.Scheme)
		}
	}
}

func TestDownloadIndexNormalizesSignedDiscordURL(t *testing.T) {
	downloadIndexMu.Lock()
	previous := downloadIndex
	downloadIndex = map[string]struct{}{
		downloadIndexKey("https://cdn.discordapp.com/attachments/1/2/file.png?ex=aaa&is=bbb&hm=ccc", "channel"): {},
	}
	downloadIndexMu.Unlock()
	t.Cleanup(func() {
		downloadIndexMu.Lock()
		downloadIndex = previous
		downloadIndexMu.Unlock()
	})

	if !dbHasDownload("https://cdn.discordapp.com/attachments/1/2/file.png?ex=ddd&is=eee&hm=fff", "channel") {
		t.Fatal("refreshed Discord attachment signature should hit the download index")
	}
	if dbHasDownload("https://cdn.discordapp.com/attachments/1/2/file.png", "other-channel") {
		t.Fatal("download index must remain scoped to the channel")
	}
}

func TestDatabaseHelpersHandleClosedDatabase(t *testing.T) {
	databaseMu.Lock()
	previous := myDB
	myDB = nil
	databaseMu.Unlock()
	t.Cleanup(func() {
		databaseMu.Lock()
		myDB = previous
		databaseMu.Unlock()
	})

	if err := dbInsertDownload(&downloadItem{}); err == nil {
		t.Fatal("dbInsertDownload() unexpectedly succeeded with a closed database")
	}
	if got := dbFindDownloadByID(1); got != nil {
		t.Fatalf("dbFindDownloadByID() = %#v, want nil", got)
	}
	if got := dbFindDownloadByURL("https://example.com/file"); len(got) != 0 {
		t.Fatalf("dbFindDownloadByURL() returned %d results with a closed database", len(got))
	}
	if got := dbDownloadCount(); got != 0 {
		t.Fatalf("dbDownloadCount() = %d, want 0", got)
	}
	if got := dbDownloadCountByChannel("channel"); got != 0 {
		t.Fatalf("dbDownloadCountByChannel() = %d, want 0", got)
	}
	dbDeleteByChannelID("channel")
}

func TestDatabaseInsertQueryAndDeleteStayConsistent(t *testing.T) {
	temporaryRoot := t.TempDir()
	testDB, err := db.OpenDB(filepath.Join(temporaryRoot, "database"))
	if err != nil {
		t.Fatal(err)
	}
	if err := testDB.Create("Downloads"); err != nil {
		testDB.Close()
		t.Fatal(err)
	}
	for _, column := range []string{"URL", "ChannelID"} {
		if err := testDB.Use("Downloads").Index([]string{column}); err != nil {
			testDB.Close()
			t.Fatal(err)
		}
	}

	databaseMu.Lock()
	previousDB := myDB
	myDB = testDB
	databaseMu.Unlock()
	downloadIndexMu.Lock()
	previousIndex := downloadIndex
	downloadIndex = make(map[string]struct{})
	downloadIndexMu.Unlock()
	previousCount := databaseDownloadCount.Load()
	duploMu.Lock()
	previousDuploCatalog := duploCatalog
	duploMu.Unlock()
	previousDuploPath := pathCacheDuplo
	pathCacheDuplo = filepath.Join(temporaryRoot, "duplo.gob")
	t.Cleanup(func() {
		databaseMu.Lock()
		testDB.Close()
		myDB = previousDB
		databaseMu.Unlock()
		downloadIndexMu.Lock()
		downloadIndex = previousIndex
		downloadIndexMu.Unlock()
		databaseDownloadCount.Store(previousCount)
		duploMu.Lock()
		duploCatalog = previousDuploCatalog
		duploMu.Unlock()
		pathCacheDuplo = previousDuploPath
	})

	signedURL := "https://cdn.discordapp.com/attachments/1/2/file.png?ex=aaa&is=bbb&hm=ccc"
	if err := dbInsertDownload(&downloadItem{URL: signedURL, ChannelID: "a", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := dbInsertDownload(&downloadItem{URL: "https://example.com/other", ChannelID: "b", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	refreshedURL := "https://cdn.discordapp.com/attachments/1/2/file.png?ex=ddd&is=eee&hm=fff"
	if got := dbFindDownloadByURL(refreshedURL); len(got) != 1 || got[0].ChannelID != "a" {
		t.Fatalf("normalized URL query returned %#v", got)
	}
	if got := dbCachedDownloadCount(); got != 2 {
		t.Fatalf("cached count = %d, want 2", got)
	}

	dbDeleteByChannelID("a")
	if dbHasDownload(refreshedURL, "a") {
		t.Fatal("deleted URL remained in the in-memory index")
	}
	if got := dbDownloadCount(); got != 1 {
		t.Fatalf("database count after delete = %d, want 1", got)
	}
	if got := dbCachedDownloadCount(); got != 1 {
		t.Fatalf("cached count after delete = %d, want 1", got)
	}
}

func TestEffectiveHistoryDownloadWorkersHonorsGlobalLimit(t *testing.T) {
	oldWorkers := config.HistoryDownloadWorkers
	oldJobs := config.HistoryMaxJobs
	oldLimit := config.ProcessLimit
	config.HistoryDownloadWorkers = 20
	config.HistoryMaxJobs = 4
	config.ProcessLimit = 12
	t.Cleanup(func() {
		config.HistoryDownloadWorkers = oldWorkers
		config.HistoryMaxJobs = oldJobs
		config.ProcessLimit = oldLimit
	})
	if got := effectiveHistoryDownloadWorkers(); got != 3 {
		t.Fatalf("effectiveHistoryDownloadWorkers() = %d, want 3", got)
	}
}

func TestDownloadManagerStopIsIdempotent(t *testing.T) {
	previous := currentDownloadManager()
	startDownloadManager(2, 2)
	manager := currentDownloadManager()
	manager.stopAndWait()
	manager.stopAndWait()
	setDownloadManager(previous)
}

func TestDownloadManagerWaitsForReservedStandaloneDownload(t *testing.T) {
	previous := currentDownloadManager()
	startDownloadManager(1, 1)
	manager := currentDownloadManager()
	t.Cleanup(func() { setDownloadManager(previous) })
	if !manager.reserve("https://cdn.example.com/file.png", "channel") {
		t.Fatal("failed to reserve standalone download")
	}

	stopped := make(chan struct{})
	go func() {
		manager.stopAndWait()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("manager stopped before the reserved download was released")
	case <-time.After(25 * time.Millisecond):
	}
	manager.release(downloadQueueKey("https://cdn.example.com/file.png", "channel"))
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("manager did not stop after the reserved download was released")
	}
}

func TestDownloadRequestQueueKeySupportsMessageLessDownloads(t *testing.T) {
	request := downloadRequestStruct{InputURL: "https://cdn.discordapp.com/emojis/123"}
	if got, want := downloadRequestQueueKey(request), downloadQueueKey(request.InputURL, "0"); got != want {
		t.Fatalf("downloadRequestQueueKey() = %q, want %q", got, want)
	}
	request.Channel = &discordgo.Channel{ID: "channel-123"}
	if got, want := downloadRequestQueueKey(request), downloadQueueKey(request.InputURL, "channel-123"); got != want {
		t.Fatalf("downloadRequestQueueKey() with channel = %q, want %q", got, want)
	}
}

func TestNonEmojiDownloadWithoutMessageFailsGracefully(t *testing.T) {
	status, _ := (downloadRequestStruct{ManualDownload: true}).tryDownload()
	if status.Status != downloadFailedInvalidSource || status.Error == nil {
		t.Fatalf("tryDownload() status = %+v, want invalid source error", status)
	}
}

func TestDownloadDomainAllowed(t *testing.T) {
	allowed := []string{"cdn.example.com"}
	blocked := []string{"bad.example.com"}
	filters := &configurationSourceFilters{
		AllowedDomains: &allowed,
		BlockedDomains: &blocked,
	}

	if !downloadDomainAllowed("CDN.EXAMPLE.COM", filters) {
		t.Fatal("expected allowed domain to be case insensitive")
	}
	if downloadDomainAllowed("bad.example.com", filters) {
		t.Fatal("expected blocked domain to be rejected")
	}
	if downloadDomainAllowed("other.example.com", filters) {
		t.Fatal("expected domain outside allowlist to be rejected")
	}
}

func TestDownloadStatusRetryable(t *testing.T) {
	for _, status := range []downloadStatus{
		downloadFailedCode,
		downloadFailedRequesting,
		downloadFailedDownloadingResponse,
		downloadFailedReadResponse,
		downloadFailedWritingFile,
	} {
		if !downloadStatusRetryable(status) {
			t.Fatalf("expected status %d to be retryable", status)
		}
	}
	for _, status := range []downloadStatus{
		downloadFailedCode403,
		downloadFailedCode404,
		downloadFailedInvalidPath,
		downloadFailedFileTooLarge,
		downloadFailedWritingDatabase,
	} {
		if downloadStatusRetryable(status) {
			t.Fatalf("expected status %d to be permanent", status)
		}
	}
}

func TestShouldLogLinkStatusHonorsBothFlags(t *testing.T) {
	logDownloads := false
	logFailures := true
	logConfig := &configurationSourceLog{LogDownloads: &logDownloads, LogFailures: &logFailures}
	if shouldLogLinkStatus(downloadSuccess, logConfig) {
		t.Fatal("successful download was logged despite logDownloads=false")
	}
	if !shouldLogLinkStatus(downloadFailedCode, logConfig) {
		t.Fatal("failed download was not logged despite logFailures=true")
	}
	logDownloads = true
	logFailures = false
	if !shouldLogLinkStatus(downloadSuccess, logConfig) {
		t.Fatal("successful download was not logged despite logDownloads=true")
	}
	if shouldLogLinkStatus(downloadFailedCode, logConfig) {
		t.Fatal("failed download was logged despite logFailures=false")
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(path, []byte("complete"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "complete" {
		t.Fatalf("unexpected file content %q", string(data))
	}
}

func TestMessageEventDeduperSeparatesEventTypes(t *testing.T) {
	deduper := newMessageEventDeduper()
	if !deduper.first("create", "message-1") {
		t.Fatal("first create should be accepted")
	}
	if deduper.first("create", "message-1") {
		t.Fatal("duplicate create should be ignored")
	}
	if !deduper.first("update", "message-1") {
		t.Fatal("update should not be suppressed by create")
	}
	for key := range deduper.seen {
		deduper.seen[key] = time.Now().Add(-3 * time.Minute)
	}
	if !deduper.first("create", "message-2") {
		t.Fatal("new message should be accepted after pruning")
	}
}
