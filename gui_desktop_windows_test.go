//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"unsafe"
)

func TestDesktopCloseChoiceMapping(t *testing.T) {
	for _, test := range []struct {
		button int32
		want   desktopCloseChoice
	}{
		{button: desktopCloseButtonHide, want: desktopCloseHide},
		{button: desktopCloseButtonExit, want: desktopCloseExit},
		{button: desktopIDYes, want: desktopCloseHide},
		{button: desktopIDNo, want: desktopCloseExit},
		{button: 0, want: desktopCloseCancel},
		{button: 2, want: desktopCloseCancel},
	} {
		if got := desktopCloseChoiceFromButton(test.button); got != test.want {
			t.Errorf("desktopCloseChoiceFromButton(%d) = %d, want %d", test.button, got, test.want)
		}
	}
}

func TestDesktopTrayToggleLabel(t *testing.T) {
	if got := desktopTrayToggleLabel(true); got != "Hide window" {
		t.Fatalf("visible tray action = %q, want Hide window", got)
	}
	if got := desktopTrayToggleLabel(false); got != "Show window" {
		t.Fatalf("hidden tray action = %q, want Show window", got)
	}
}

func TestDesktopTrayStatusLabel(t *testing.T) {
	for _, test := range []struct {
		name     string
		snapshot desktopTraySnapshot
		want     string
	}{
		{
			name:     "waiting",
			snapshot: desktopTraySnapshot{Runtime: "waiting"},
			want:     "Status: waiting for Start",
		},
		{
			name:     "starting",
			snapshot: desktopTraySnapshot{Runtime: "starting"},
			want:     "Status: starting",
		},
		{
			name:     "running idle",
			snapshot: desktopTraySnapshot{Runtime: "running"},
			want:     "Status: running - no transfers",
		},
		{
			name:     "running transfers",
			snapshot: desktopTraySnapshot{Runtime: "running", Active: 2, Queued: 3},
			want:     "Status: running - 2 active, 3 queued",
		},
		{
			name:     "stopping",
			snapshot: desktopTraySnapshot{Runtime: "stopping"},
			want:     "Status: stopping",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := desktopTrayStatusLabel(test.snapshot); got != test.want {
				t.Fatalf("desktopTrayStatusLabel(%+v) = %q, want %q", test.snapshot, got, test.want)
			}
		})
	}
}

func TestDesktopExitConfirmationMessage(t *testing.T) {
	idle := desktopExitConfirmationMessage(desktopTraySnapshot{})
	if idle != "The application, Discord connection, and download processes will be stopped safely." {
		t.Fatalf("idle exit confirmation = %q", idle)
	}

	busy := desktopExitConfirmationMessage(desktopTraySnapshot{Active: 2, Queued: 3})
	want := "There are still 2 active and 3 queued downloads. Exiting will safely stop the application and those transfers."
	if busy != want {
		t.Fatalf("busy exit confirmation = %q, want %q", busy, want)
	}
}

func TestDesktopTaskDialogStructLayout(t *testing.T) {
	pointerSize := unsafe.Sizeof(uintptr(0))
	wantConfigSize := uintptr(96)
	wantButtonSize := uintptr(8)
	if pointerSize == 8 {
		wantConfigSize = 176
		wantButtonSize = 16
	}
	if got := unsafe.Sizeof(desktopTaskDialogConfig{}); got != wantConfigSize {
		t.Fatalf("TASKDIALOGCONFIG size = %d, want %d for %d-byte pointers", got, wantConfigSize, pointerSize)
	}
	if got := unsafe.Sizeof(desktopTaskDialogButton{}); got != wantButtonSize {
		t.Fatalf("TASKDIALOG_BUTTON size = %d, want %d for %d-byte pointers", got, wantButtonSize, pointerSize)
	}
}

func TestDesktopGUIWindowLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>desktop check</title><p>ready</p>"))
	}))
	defer server.Close()

	if !launchDesktopGUI(server.URL) {
		t.Fatal("WebView2 desktop window could not be created")
	}
	shutdownDesktopGUI()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		desktopGUIMu.Lock()
		closed := desktopGUIWindow == nil
		desktopGUIMu.Unlock()
		if closed {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("WebView2 desktop window did not close")
}
