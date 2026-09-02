package main

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HouzuoGuo/tiedot/db"
)

func installDownloadTestDatabase(t *testing.T, temporaryRoot string) {
	t.Helper()
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

	oldConfig := config
	config = defaultConfiguration()
	databaseMu.Lock()
	oldDB := myDB
	myDB = testDB
	databaseMu.Unlock()
	downloadIndexMu.Lock()
	oldIndex := downloadIndex
	downloadIndex = make(map[string]struct{})
	downloadIndexMu.Unlock()
	oldCount := databaseDownloadCount.Load()
	t.Cleanup(func() {
		config = oldConfig
		databaseMu.Lock()
		testDB.Close()
		myDB = oldDB
		databaseMu.Unlock()
		downloadIndexMu.Lock()
		downloadIndex = oldIndex
		downloadIndexMu.Unlock()
		databaseDownloadCount.Store(oldCount)
	})
}

// TestDownloadEndToEndWithEncodedFilename exercises a real HTTP listener,
// streaming download, atomic filesystem promotion, and database indexing.
func TestDownloadEndToEndWithEncodedFilename(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	filenames := map[string]string{
		"my photo.png":                    "my photo.png",
		"日本語-한국어-中文.png":                  "日本語-한국어-中文.png",
		strings.Repeat("界", 100) + ".png": strings.Repeat("界", 83) + ".png",
		"content-disposition.png":         "C++ Guide.png",
		"rfc5987-unicode.png":             "日本語-한국어-中文%20raw.png",
		"PHOTO.JFIF":                      "PHOTO.jpg",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if _, exists := filenames[strings.TrimPrefix(request.URL.Path, "/")]; !exists {
			http.NotFound(w, request)
			return
		}
		if request.URL.Path == "/content-disposition.png" {
			w.Header().Set("Content-Disposition", `attachment; filename="C++ Guide.png"`)
		}
		if request.URL.Path == "/rfc5987-unicode.png" {
			w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%97%A5%E6%9C%AC%E8%AA%9E-%ED%95%9C%EA%B5%AD%EC%96%B4-%E4%B8%AD%E6%96%87%2520raw.png")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData.Bytes())
	}))
	defer server.Close()

	temporaryRoot := t.TempDir()
	installDownloadTestDatabase(t, temporaryRoot)

	destination := filepath.Join(temporaryRoot, "downloads")
	for sourceName, savedName := range filenames {
		t.Run(sourceName, func(t *testing.T) {
			inputURL := server.URL + "/" + url.PathEscape(sourceName) + "#first-preview"
			status, size := (downloadRequestStruct{
				InputURL:  inputURL,
				Path:      destination,
				EmojiCmd:  true,
				FileTime:  time.Now(),
				StartTime: time.Now(),
			}).tryDownload()
			if status.Status != downloadSuccess {
				t.Fatalf("tryDownload() status = %+v, want downloadSuccess", status)
			}
			if size != int64(imageData.Len()) {
				t.Fatalf("tryDownload() size = %d, want %d", size, imageData.Len())
			}

			savedPath := filepath.Join(destination, savedName)
			saved, err := os.ReadFile(savedPath)
			if err != nil {
				t.Fatalf("downloaded file was not saved with the decoded Unicode name: %v", err)
			}
			if !bytes.Equal(saved, imageData.Bytes()) {
				t.Fatal("downloaded file content differs from the HTTP response")
			}
			if !dbHasDownload(server.URL+"/"+url.PathEscape(sourceName)+"#another-preview", "0") {
				t.Fatal("database index treated fragments of the same HTTP resource as different downloads")
			}
		})
	}
}

func TestDownloadEndToEndSaveOtherIncludesFontMediaTypes(t *testing.T) {
	fontData := append([]byte("wOFF"), make([]byte, 64)...)
	if detected := http.DetectContentType(fontData); detected != "font/woff" {
		t.Fatalf("test fixture detected as %q, want font/woff", detected)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fontData)
	}))
	defer server.Close()

	temporaryRoot := t.TempDir()
	installDownloadTestDatabase(t, temporaryRoot)
	config.SaveOtherFiles = true
	destination := filepath.Join(temporaryRoot, "downloads")
	status, size := (downloadRequestStruct{
		InputURL:  server.URL + "/font.woff",
		Path:      destination,
		EmojiCmd:  true,
		FileTime:  time.Now(),
		StartTime: time.Now(),
	}).tryDownload()
	if status.Status != downloadSuccess {
		t.Fatalf("tryDownload() status = %+v, want downloadSuccess with saveOtherFiles", status)
	}
	if size != int64(len(fontData)) {
		t.Fatalf("tryDownload() size = %d, want %d", size, len(fontData))
	}
	if saved, err := os.ReadFile(filepath.Join(destination, "font.woff")); err != nil {
		t.Fatalf("font file was not saved: %v", err)
	} else if !bytes.Equal(saved, fontData) {
		t.Fatal("saved font differs from HTTP response")
	}
}

func TestDownloadDuplicateSuffixRespectsFilenameLimit(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData.Bytes())
	}))
	defer server.Close()

	temporaryRoot := t.TempDir()
	installDownloadTestDatabase(t, temporaryRoot)
	config.SavePossibleDuplicates = true
	destination := filepath.Join(temporaryRoot, "downloads")
	sourceName := strings.Repeat("a", 251) + ".png"
	request := downloadRequestStruct{
		InputURL:  server.URL + "/" + sourceName,
		Path:      destination,
		EmojiCmd:  true,
		FileTime:  time.Now(),
		StartTime: time.Now(),
	}
	for attempt := 0; attempt < 2; attempt++ {
		status, _ := request.tryDownload()
		if status.Status != downloadSuccess {
			t.Fatalf("duplicate download %d status = %+v, want downloadSuccess", attempt+1, status)
		}
	}

	duplicateName := strings.Repeat("a", 249) + "-1.png"
	if len(duplicateName) != 255 {
		t.Fatalf("invalid test fixture length %d", len(duplicateName))
	}
	if _, err := os.Stat(filepath.Join(destination, duplicateName)); err != nil {
		t.Fatalf("bounded duplicate filename was not saved: %v", err)
	}
}

func TestDownloadMaxFileSizeAtInt64LimitDoesNotOverflow(t *testing.T) {
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(imageData.Bytes())
	}))
	defer server.Close()

	temporaryRoot := t.TempDir()
	installDownloadTestDatabase(t, temporaryRoot)
	config.DownloadMaxFileSize = math.MaxInt64
	status, size := (downloadRequestStruct{
		InputURL:  server.URL + "/image.png",
		Path:      filepath.Join(temporaryRoot, "downloads"),
		EmojiCmd:  true,
		FileTime:  time.Now(),
		StartTime: time.Now(),
	}).tryDownload()
	if status.Status != downloadSuccess || size != int64(imageData.Len()) {
		t.Fatalf("tryDownload() = %+v, %d bytes; want success and %d bytes", status, size, imageData.Len())
	}
}
