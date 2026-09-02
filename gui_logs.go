package main

import (
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const guiLogCapacity = 1000

var (
	guiANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	guiRuntimeLogs  = &guiLogBuffer{}
)

type guiLogEntry struct {
	Sequence uint64 `json:"seq"`
	Text     string `json:"text"`
}

type guiLogBuffer struct {
	mu      sync.RWMutex
	next    uint64
	entries []guiLogEntry
}

func initGUILogCapture(output io.Writer) {
	// Keep the ordinary terminal output while also retaining a bounded copy for
	// the in-app console. Put the GUI writer first so Windows GUI builds still
	// retain logs when no terminal handle exists.
	log.SetOutput(io.MultiWriter(guiRuntimeLogs, output))
}

func (buffer *guiLogBuffer) Write(content []byte) (int, error) {
	plain := guiANSISequence.ReplaceAllString(string(content), "")
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	plain = strings.TrimSuffix(plain, "\n")

	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	for _, line := range strings.Split(plain, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		buffer.next++
		buffer.entries = append(buffer.entries, guiLogEntry{Sequence: buffer.next, Text: line})
	}
	if overflow := len(buffer.entries) - guiLogCapacity; overflow > 0 {
		copy(buffer.entries, buffer.entries[overflow:])
		buffer.entries = buffer.entries[:guiLogCapacity]
	}
	return len(content), nil
}

func (buffer *guiLogBuffer) after(sequence uint64) ([]guiLogEntry, uint64) {
	buffer.mu.RLock()
	defer buffer.mu.RUnlock()
	entries := make([]guiLogEntry, 0, len(buffer.entries))
	for _, entry := range buffer.entries {
		if entry.Sequence > sequence {
			entries = append(entries, entry)
		}
	}
	return entries, buffer.next
}

func guiLogsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		guiMethodNotAllowed(w, http.MethodGet)
		return
	}
	if !guiAuthorized(r) {
		guiWriteError(w, http.StatusForbidden, "invalid GUI request token")
		return
	}
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil && r.URL.Query().Get("since") != "" {
		guiWriteError(w, http.StatusBadRequest, "invalid log cursor")
		return
	}
	entries, next := guiRuntimeLogs.after(since)
	guiWriteJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
		"next":    next,
	})
}
