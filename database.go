package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HouzuoGuo/tiedot/db"
	"github.com/fatih/color"
	"github.com/rivo/duplo"
)

var (
	databaseMu            sync.Mutex
	databaseDownloadCount atomic.Int64
	downloadIndexMu       sync.RWMutex
	downloadIndex         = make(map[string]struct{})
	duploMu               sync.Mutex
)

func downloadIndexKey(inputURL, channelID string) string {
	return channelID + "\x00" + normalizeDownloadURL(inputURL)
}

func rebuildDownloadIndex() int {
	index := make(map[string]struct{})
	count := 0
	databaseMu.Lock()
	if myDB != nil && myDB.Use("Downloads") != nil {
		myDB.Use("Downloads").ForEachDoc(func(_ int, docContent []byte) bool {
			count++
			var document map[string]interface{}
			if json.Unmarshal(docContent, &document) == nil {
				inputURL := stringValue(document["URL"])
				channelID := stringValue(document["ChannelID"])
				if inputURL != "" && channelID != "" {
					index[downloadIndexKey(inputURL, channelID)] = struct{}{}
				}
			}
			return true
		})
	}
	downloadIndexMu.Lock()
	downloadIndex = index
	downloadIndexMu.Unlock()
	databaseDownloadCount.Store(int64(count))
	databaseMu.Unlock()
	return count
}

func dbHasDownload(inputURL, channelID string) bool {
	downloadIndexMu.RLock()
	_, exists := downloadIndex[downloadIndexKey(inputURL, channelID)]
	downloadIndexMu.RUnlock()
	return exists
}

func databaseReady() bool {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return myDB != nil && myDB.Use("Downloads") != nil
}

func closeDatabase() {
	databaseMu.Lock()
	if myDB != nil {
		myDB.Close()
		myDB = nil
	}
	databaseMu.Unlock()
}

func clearDuploCatalog() {
	duploMu.Lock()
	duploCatalog = duplo.New()
	duploMu.Unlock()
	if err := os.Remove(pathCacheDuplo); err != nil && !os.IsNotExist(err) {
		log.Println(lg("Duplo", "Reset", color.HiRedString, "Unable to remove stale catalog: %s", err))
	}
}

func highestDuploDownloadID() int {
	highest := 0
	duploMu.Lock()
	defer duploMu.Unlock()
	if duploCatalog == nil {
		return highest
	}
	for _, id := range duploCatalog.IDs() {
		if numericID, ok := id.(int); ok && numericID > highest {
			highest = numericID
		}
	}
	return highest
}

func openDatabase() {
	var openT time.Time
	var createT time.Time
	// Database
	log.Println(lg("Database", "", color.YellowString, "Opening database...\t(this can take a bit...)"))
	openT = time.Now()
	openedDB, openErr := db.OpenDB(pathDatabaseBase)
	if openErr != nil {
		log.Println(lg("Database", "", color.HiRedString, "Unable to open database: %s", openErr))
		return
	}
	if openedDB.Use("Downloads") == nil {
		log.Println(lg("Database", "Setup", color.YellowString, "Creating database, please wait..."))
		createT = time.Now()
		if err := openedDB.Create("Downloads"); err != nil {
			log.Println(lg("Database", "Setup", color.HiRedString, "Error while trying to create database: %s", err))
			openedDB.Close()
			return
		}
		log.Println(lg("Database", "Setup", color.HiYellowString, "Created new database...\t(took %s)", timeSinceShort(createT)))
		//
		log.Println(lg("Database", "Setup", color.YellowString, "Structuring database, please wait..."))
		createT = time.Now()
		indexColumn := func(col string) {
			if err := openedDB.Use("Downloads").Index([]string{col}); err != nil {
				log.Println(lg("Database", "Setup", color.HiRedString, "Unable to create index for %s: %s", col, err))
				return
			}
		}
		indexColumn("URL")
		indexColumn("ChannelID")
		indexColumn("UserID")
		log.Println(lg("Database", "Setup", color.HiYellowString, "Created database structure...\t(took %s)", timeSinceShort(createT)))
	}
	databaseMu.Lock()
	if myDB != nil && myDB != openedDB {
		myDB.Close()
	}
	myDB = openedDB
	databaseMu.Unlock()
	// Cache download tally
	downloadCount := rebuildDownloadIndex()
	log.Println(lg("Database", "", color.HiYellowString, "Database opened, contains %d entries...\t(took %s)", downloadCount, timeSinceShort(openT)))

	// Duplo
	if configUsesDuplo() {
		log.Println(lg("Duplo", "", color.HiRedString, "!!! Duplo is barely supported and may cause issues, use at your own risk..."))
		loadedCatalog := duplo.New()
		if _, err := os.Stat(pathCacheDuplo); err == nil {
			log.Println(lg("Duplo", "", color.YellowString, "Opening duplo image catalog..."))
			openT = time.Now()
			storeFile, err := os.ReadFile(pathCacheDuplo)
			if err != nil {
				log.Println(lg("Duplo", "", color.HiRedString, "Error opening duplo catalog:\t%s", err))
			} else {
				err = loadedCatalog.GobDecode(storeFile)
				if err != nil {
					log.Println(lg("Duplo", "", color.HiRedString, "Error decoding duplo catalog:\t%s", err))
				}
				log.Println(lg("Duplo", "", color.HiYellowString, "Duplo catalog opened (%d)\t(took %s)", loadedCatalog.Size(), timeSinceShort(openT)))
			}
		}
		duploMu.Lock()
		duploCatalog = loadedCatalog
		duploMu.Unlock()
	}
	// Download attempts can consume IDs without creating database records. Use
	// the largest persisted Duplo ID as well as the row count to avoid reusing
	// an existing visual-hash identifier after restart.
	setCachedDownloadID(max(downloadCount, highestDuploDownloadID()))
}

func resetDownloadDatabase() error {
	if historyJobs != nil {
		for _, pair := range historyJobs.Snapshot() {
			switch pair.Value.Status {
			case historyStatusWaiting, historyStatusRunning, historyStatusAbortRequested:
				return fmt.Errorf("history job %s is still active", pair.Key)
			}
		}
	}

	// Stop and drain real-time work before closing the database. New gateway
	// events see the stopped manager and are rejected until a fresh manager is
	// installed after the reset.
	previousManager := currentDownloadManager()
	if previousManager != nil {
		previousManager.stopAndWait()
	}

	databaseMu.Lock()
	if myDB != nil {
		myDB.Close()
		myDB = nil
	}
	removeErr := os.RemoveAll(pathDatabaseBase)
	databaseMu.Unlock()
	clearDuploCatalog()

	// Even after a failed deletion, try reopening the existing path so the
	// runtime is not left with a closed database.
	openDatabase()
	if !databaseReady() {
		if removeErr != nil {
			return fmt.Errorf("delete database: %w (database reopen also failed)", removeErr)
		}
		return fmt.Errorf("database reopen failed")
	}
	if previousManager != nil {
		startDownloadManager(config.DownloadWorkers, config.DownloadQueueSize)
	}
	return removeErr
}

func saveDuploCatalog() error {
	duploMu.Lock()
	defer duploMu.Unlock()
	if duploCatalog == nil {
		return nil
	}
	data, err := duploCatalog.GobEncode()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(pathCacheDuplo), 0755); err != nil {
		return err
	}
	return writeFileAtomic(pathCacheDuplo, data, 0600)
}

func backupDatabase() error {
	if err := os.MkdirAll(pathDatabaseBackups, 0755); err != nil {
		return err
	}
	backupPath := filepath.Join(pathDatabaseBackups, time.Now().Format("2006-01-02_15-04-05.000000000")+".zip")
	file, err := os.Create(backupPath)
	if err != nil {
		return err
	}
	w := zip.NewWriter(file)
	databaseMu.Lock()
	err = filepath.Walk(pathDatabaseBase, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		relativePath, err := filepath.Rel(pathDatabaseBase, path)
		if err != nil {
			source.Close()
			return err
		}
		destination, err := w.Create(filepath.ToSlash(relativePath))
		if err == nil {
			_, err = io.Copy(destination, source)
		}
		closeErr := source.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	databaseMu.Unlock()
	if err != nil {
		w.Close()
		file.Close()
		os.Remove(backupPath)
		return err
	}
	if err := w.Close(); err != nil {
		file.Close()
		os.Remove(backupPath)
		return err
	}
	if err := file.Close(); err != nil {
		os.Remove(backupPath)
		return err
	}
	return nil
}

//#region Database Utility

func dbInsertDownload(download *downloadItem) error {
	if download == nil {
		return fmt.Errorf("download record is nil")
	}
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if myDB == nil || myDB.Use("Downloads") == nil {
		return fmt.Errorf("downloads database is not open")
	}
	_, err := myDB.Use("Downloads").Insert(map[string]interface{}{
		"URL":         normalizeDownloadURL(download.URL),
		"Time":        download.Time.Format(time.RFC3339Nano),
		"Destination": download.Destination,
		"Filename":    download.Filename,
		"ChannelID":   download.ChannelID,
		"UserID":      download.UserID,
	})
	if err == nil {
		downloadIndexMu.Lock()
		downloadIndex[downloadIndexKey(download.URL, download.ChannelID)] = struct{}{}
		downloadIndexMu.Unlock()
		databaseDownloadCount.Add(1)
	}
	return err
}

func dbFindDownloadByID(id int) *downloadItem {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	return dbFindDownloadByIDLocked(id)
}

func dbFindDownloadByIDLocked(id int) *downloadItem {
	if myDB == nil {
		return nil
	}
	downloads := myDB.Use("Downloads")
	if downloads == nil {
		return nil
	}
	readBack, err := downloads.Read(id)
	if err != nil {
		log.Println(lg("Database", "Downloads", color.HiRedString, "Failed to read database:\t%s", err))
		return nil
	}
	timeString, ok := readBack["Time"].(string)
	if !ok {
		return nil
	}
	timeT, parseErr := time.Parse(time.RFC3339Nano, timeString)
	if parseErr != nil {
		// Older records may include a process-local monotonic clock suffix.
		wallTime := strings.SplitN(timeString, " m=", 2)[0]
		timeT, _ = time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", wallTime)
	}
	return &downloadItem{
		URL:         stringValue(readBack["URL"]),
		Time:        timeT,
		Destination: stringValue(readBack["Destination"]),
		Filename:    stringValue(readBack["Filename"]),
		ChannelID:   stringValue(readBack["ChannelID"]),
		UserID:      stringValue(readBack["UserID"]),
	}
}

func stringValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func dbFindDownloadByURL(inputURL string) []*downloadItem {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if myDB == nil || myDB.Use("Downloads") == nil {
		return nil
	}
	encodedURL, _ := json.Marshal(normalizeDownloadURL(inputURL))
	var query interface{}
	if err := json.Unmarshal([]byte(fmt.Sprintf(`[{"eq": %s, "in": ["URL"]}]`, encodedURL)), &query); err != nil {
		return nil
	}
	queryResult := make(map[int]struct{})
	if err := db.EvalQuery(query, myDB.Use("Downloads"), &queryResult); err != nil {
		log.Println(lg("Database", "Downloads", color.HiRedString, "Failed to query database:\t%s", err))
		return nil
	}

	downloadedImages := make([]*downloadItem, 0)
	for id := range queryResult {
		if download := dbFindDownloadByIDLocked(id); download != nil {
			downloadedImages = append(downloadedImages, download)
		}
	}
	return downloadedImages
}

func dbDeleteByChannelID(channelID string) {
	databaseMu.Lock()
	if myDB == nil || myDB.Use("Downloads") == nil {
		databaseMu.Unlock()
		return
	}
	encodedChannelID, _ := json.Marshal(channelID)
	var query interface{}
	if err := json.Unmarshal([]byte(fmt.Sprintf(`[{"eq": %s, "in": ["ChannelID"]}]`, encodedChannelID)), &query); err != nil {
		databaseMu.Unlock()
		return
	}
	queryResult := make(map[int]struct{})
	if err := db.EvalQuery(query, myDB.Use("Downloads"), &queryResult); err != nil {
		databaseMu.Unlock()
		log.Println(lg("Database", "Downloads", color.HiRedString, "Failed to query database:\t%s", err))
		return
	}
	for id := range queryResult {
		if err := myDB.Use("Downloads").Delete(id); err != nil {
			log.Println(lg("Database", "Downloads", color.HiRedString, "Failed to delete database entry %d:\t%s", id, err))
		}
	}
	databaseMu.Unlock()
	if len(queryResult) > 0 {
		// Duplo IDs are not tied to database document IDs, so selectively
		// removing only this channel is not reliable. Clearing the catalog is
		// safer than retaining hashes for records the user explicitly deleted.
		clearDuploCatalog()
	}
	// Rebuild from the database so failed/partial deletes cannot desynchronise
	// the in-memory URL index or cached count.
	rebuildDownloadIndex()
}

//#endregion

//#region Statistics

func dbDownloadCount() int {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if myDB == nil || myDB.Use("Downloads") == nil {
		return 0
	}
	i := 0
	myDB.Use("Downloads").ForEachDoc(func(id int, docContent []byte) (willMoveOn bool) {
		i++
		return true
	})
	return i
}

func dbDownloadCountByChannel(channelID string) int {
	databaseMu.Lock()
	defer databaseMu.Unlock()
	if myDB == nil || myDB.Use("Downloads") == nil {
		return 0
	}
	encodedChannelID, _ := json.Marshal(channelID)
	var query interface{}
	if err := json.Unmarshal([]byte(fmt.Sprintf(`[{"eq": %s, "in": ["ChannelID"]}]`, encodedChannelID)), &query); err != nil {
		return 0
	}
	queryResult := make(map[int]struct{})
	if err := db.EvalQuery(query, myDB.Use("Downloads"), &queryResult); err != nil {
		return 0
	}
	return len(queryResult)
}

func dbCachedDownloadCount() int64 {
	return databaseDownloadCount.Load()
}

//#endregion
