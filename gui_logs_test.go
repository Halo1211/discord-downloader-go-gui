package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGUILogBufferStripsColorsAndKeepsCursor(t *testing.T) {
	buffer := &guiLogBuffer{}
	_, _ = buffer.Write([]byte("\x1b[32mfirst line\x1b[0m\nsecond line\n"))

	entries, next := buffer.after(0)
	if next != 2 || len(entries) != 2 {
		t.Fatalf("unexpected log snapshot: next=%d entries=%+v", next, entries)
	}
	if entries[0].Text != "first line" || entries[1].Text != "second line" {
		t.Fatalf("ANSI formatting was not removed: %+v", entries)
	}
	entries, next = buffer.after(1)
	if next != 2 || len(entries) != 1 || entries[0].Sequence != 2 {
		t.Fatalf("incremental cursor returned the wrong entries: %+v", entries)
	}
}

func TestGUILogsEndpointRequiresTokenAndReturnsEntries(t *testing.T) {
	oldBuffer := guiRuntimeLogs
	oldToken := guiToken
	guiRuntimeLogs = &guiLogBuffer{}
	guiToken = "console-token"
	t.Cleanup(func() {
		guiRuntimeLogs = oldBuffer
		guiToken = oldToken
	})
	_, _ = guiRuntimeLogs.Write([]byte("runtime is ready\n"))

	denied := httptest.NewRecorder()
	guiLogsHandler(denied, httptest.NewRequest(http.MethodGet, "/api/logs?since=0", nil))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("unauthorized log request returned %d", denied.Code)
	}

	allowed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/logs?since=0", nil)
	request.Header.Set("X-DDG-GUI-Token", guiToken)
	guiLogsHandler(allowed, request)
	if allowed.Code != http.StatusOK || !strings.Contains(allowed.Body.String(), "runtime is ready") {
		t.Fatalf("unexpected log response %d: %s", allowed.Code, allowed.Body.String())
	}
}
