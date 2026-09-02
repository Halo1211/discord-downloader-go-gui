//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/fatih/color"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	desktopGUIMu          sync.Mutex
	desktopGUIWindow      webview2.WebView
	desktopGUIStopping    atomic.Bool
	desktopWindowVisible  atomic.Bool
	desktopTrayNoticeSeen atomic.Bool
	setProcessDPIAware    = windows.NewLazySystemDLL("user32.dll").NewProc("SetProcessDpiAwarenessContext")
	desktopWindowHandle   uintptr
	desktopOldWindowProc  uintptr
	desktopTrayAdded      bool
	desktopTaskbarCreated uint32
)

var (
	user32Desktop     = windows.NewLazySystemDLL("user32.dll")
	shell32Desktop    = windows.NewLazySystemDLL("shell32.dll")
	comctl32Desktop   = windows.NewLazySystemDLL("comctl32.dll")
	kernel32Desktop   = windows.NewLazySystemDLL("kernel32.dll")
	setWindowLongPtrW = func() *windows.LazyProc {
		if unsafe.Sizeof(uintptr(0)) == 4 {
			return user32Desktop.NewProc("SetWindowLongW")
		}
		return user32Desktop.NewProc("SetWindowLongPtrW")
	}()
	callWindowProcW          = user32Desktop.NewProc("CallWindowProcW")
	showWindowProc           = user32Desktop.NewProc("ShowWindow")
	setForegroundWindowProc  = user32Desktop.NewProc("SetForegroundWindow")
	loadIconW                = user32Desktop.NewProc("LoadIconW")
	createPopupMenuProc      = user32Desktop.NewProc("CreatePopupMenu")
	appendMenuWProc          = user32Desktop.NewProc("AppendMenuW")
	trackPopupMenuProc       = user32Desktop.NewProc("TrackPopupMenu")
	destroyMenuProc          = user32Desktop.NewProc("DestroyMenu")
	getCursorPosProc         = user32Desktop.NewProc("GetCursorPos")
	registerWindowMessageW   = user32Desktop.NewProc("RegisterWindowMessageW")
	messageBoxWProc          = user32Desktop.NewProc("MessageBoxW")
	setWindowsHookExWProc    = user32Desktop.NewProc("SetWindowsHookExW")
	unhookWindowsHookExProc  = user32Desktop.NewProc("UnhookWindowsHookEx")
	callNextHookExProc       = user32Desktop.NewProc("CallNextHookEx")
	setDlgItemTextWProc      = user32Desktop.NewProc("SetDlgItemTextW")
	getCurrentThreadIDProc   = kernel32Desktop.NewProc("GetCurrentThreadId")
	shellNotifyIconW         = shell32Desktop.NewProc("Shell_NotifyIconW")
	taskDialogIndirectProc   = comctl32Desktop.NewProc("TaskDialogIndirect")
	desktopWindowProcPointer = syscall.NewCallback(desktopWindowProc)
	desktopMessageBoxHookPtr = syscall.NewCallback(desktopMessageBoxHookProc)
)

var (
	desktopMessageBoxHook        uintptr
	desktopMessageBoxYesLabel    string
	desktopMessageBoxNoLabel     string
	desktopMessageBoxCancelLabel string
)

const (
	desktopWMClose         = 0x0010
	desktopWMContextMenu   = 0x007B
	desktopWMLButtonUp     = 0x0202
	desktopWMLButtonDblClk = 0x0203
	desktopWMRButtonUp     = 0x0205
	desktopNINSelect       = 0x0400
	desktopNINKeySelect    = 0x0401
	desktopTrayMessage     = 0x8000 + 17
	desktopGWLWndProc      = ^uintptr(3) // -4
	desktopSWHide          = 0
	desktopSWRestore       = 9
	desktopNIMAdd          = 0
	desktopNIMModify       = 1
	desktopNIMDelete       = 2
	desktopNIMSetVersion   = 4
	desktopNIFMessage      = 0x1
	desktopNIFIcon         = 0x2
	desktopNIFTip          = 0x4
	desktopNIFInfo         = 0x10
	desktopNIFShowTip      = 0x80
	desktopNotifyVersion4  = 4
	desktopMFString        = 0
	desktopMFGrayed        = 0x1
	desktopMFSeparator     = 0x800
	desktopTPMRightButton  = 0x2
	desktopTPMReturnCmd    = 0x100
	desktopMenuToggle      = 1
	desktopMenuExit        = 2
	desktopIDIApplication  = 32512
	desktopNIIFInfo        = 1

	desktopTDFUseCommandLinks         = 0x10
	desktopTDFAllowDialogCancellation = 0x8
	desktopTDFPositionRelative        = 0x1000
	desktopTDFSizeToContent           = 0x01000000
	desktopTDCBFCancelButton          = 0x8
	desktopCloseButtonHide            = 100
	desktopCloseButtonExit            = 101

	desktopMBYesNo        = 0x4
	desktopMBYesNoCancel  = 0x3
	desktopMBIconQuestion = 0x20
	desktopMBTaskModal    = 0x2000
	desktopIDYes          = 6
	desktopIDNo           = 7
	desktopIDCancel       = 2
	desktopWHCBT          = 5
	desktopHCBTActivate   = 5
)

type desktopCloseChoice uint8

const (
	desktopCloseCancel desktopCloseChoice = iota
	desktopCloseHide
	desktopCloseExit
)

type desktopTraySnapshot struct {
	Runtime string
	Active  int
	Queued  int
	Visible bool
}

type desktopPoint struct {
	X int32
	Y int32
}

type desktopTaskDialogButton struct {
	ID   int32
	Text *uint16
}

type desktopTaskDialogConfig struct {
	CbSize               uint32
	HWndParent           windows.HWND
	HInstance            windows.Handle
	Flags                uint32
	CommonButtons        uint32
	WindowTitle          *uint16
	MainIcon             windows.Handle
	MainInstruction      *uint16
	Content              *uint16
	ButtonCount          uint32
	Buttons              *desktopTaskDialogButton
	DefaultButton        int32
	RadioButtonCount     uint32
	RadioButtons         uintptr
	DefaultRadioButton   int32
	VerificationText     *uint16
	ExpandedInformation  *uint16
	ExpandedControlText  *uint16
	CollapsedControlText *uint16
	FooterIcon           windows.Handle
	Footer               *uint16
	Callback             uintptr
	CallbackData         uintptr
	Width                uint32
}

type desktopNotifyIconData struct {
	CbSize          uint32
	HWnd            windows.HWND
	UID             uint32
	UFlags          uint32
	CallbackMessage uint32
	HIcon           windows.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     windows.Handle
}

func desktopNotifyData() desktopNotifyIconData {
	data := desktopNotifyIconData{
		CbSize:          uint32(unsafe.Sizeof(desktopNotifyIconData{})),
		HWnd:            windows.HWND(desktopWindowHandle),
		UID:             1,
		UFlags:          desktopNIFMessage | desktopNIFIcon | desktopNIFTip | desktopNIFShowTip,
		CallbackMessage: desktopTrayMessage,
	}
	icon, _, _ := loadIconW.Call(0, desktopIDIApplication)
	data.HIcon = windows.Handle(icon)
	tip, _ := windows.UTF16FromString("Discord Downloader Go - click to open, right-click for menu")
	copy(data.Tip[:], tip)
	return data
}

func showDesktopTrayNoticeOnce() {
	if !desktopTrayAdded || desktopWindowHandle == 0 {
		return
	}
	if !desktopTrayNoticeSeen.CompareAndSwap(false, true) {
		return
	}
	data := desktopNotifyIconData{
		CbSize:    uint32(unsafe.Sizeof(desktopNotifyIconData{})),
		HWnd:      windows.HWND(desktopWindowHandle),
		UID:       1,
		UFlags:    desktopNIFInfo,
		InfoFlags: desktopNIIFInfo,
	}
	title, _ := windows.UTF16FromString("Discord Downloader Go masih berjalan")
	message, _ := windows.UTF16FromString("Window hidden in the system tray. Downloads and the Discord connection continue running.")
	copy(data.InfoTitle[:], title)
	copy(data.Info[:], message)
	_, _, _ = shellNotifyIconW.Call(desktopNIMModify, uintptr(unsafe.Pointer(&data)))
}

func desktopCloseChoiceFromButton(button int32) desktopCloseChoice {
	switch button {
	case desktopCloseButtonHide, desktopIDYes:
		return desktopCloseHide
	case desktopCloseButtonExit, desktopIDNo:
		return desktopCloseExit
	default:
		return desktopCloseCancel
	}
}

func desktopMessageBoxHookProc(code int, wParam, lParam uintptr) uintptr {
	hook := desktopMessageBoxHook
	if code == desktopHCBTActivate {
		setDesktopMessageBoxButtonText(wParam, desktopIDYes, desktopMessageBoxYesLabel)
		setDesktopMessageBoxButtonText(wParam, desktopIDNo, desktopMessageBoxNoLabel)
		setDesktopMessageBoxButtonText(wParam, desktopIDCancel, desktopMessageBoxCancelLabel)
		if hook != 0 {
			_, _, _ = unhookWindowsHookExProc.Call(hook)
			desktopMessageBoxHook = 0
		}
	}
	result, _, _ := callNextHookExProc.Call(hook, uintptr(code), wParam, lParam)
	return result
}

func setDesktopMessageBoxButtonText(dialog uintptr, buttonID int, label string) {
	if label == "" {
		return
	}
	text, _ := windows.UTF16PtrFromString(label)
	_, _, _ = setDlgItemTextWProc.Call(dialog, uintptr(buttonID), uintptr(unsafe.Pointer(text)))
}

func installDesktopMessageBoxHook(yesLabel, noLabel, cancelLabel string) {
	desktopMessageBoxYesLabel = yesLabel
	desktopMessageBoxNoLabel = noLabel
	desktopMessageBoxCancelLabel = cancelLabel
	threadID, _, _ := getCurrentThreadIDProc.Call()
	hook, _, _ := setWindowsHookExWProc.Call(desktopWHCBT, desktopMessageBoxHookPtr, 0, threadID)
	desktopMessageBoxHook = hook
}

func removeDesktopMessageBoxHook() {
	if desktopMessageBoxHook != 0 {
		_, _, _ = unhookWindowsHookExProc.Call(desktopMessageBoxHook)
		desktopMessageBoxHook = 0
	}
	desktopMessageBoxYesLabel = ""
	desktopMessageBoxNoLabel = ""
	desktopMessageBoxCancelLabel = ""
}

func promptDesktopCloseChoice(hwnd uintptr) desktopCloseChoice {
	title, _ := windows.UTF16PtrFromString("Tutup Discord Downloader Go?")
	instruction, _ := windows.UTF16PtrFromString("Choose hide or exit")
	content, _ := windows.UTF16PtrFromString("Hiding keeps downloads and the Discord connection running. Exit stops the application safely.")
	hideText, _ := windows.UTF16PtrFromString("Hide to the system tray\nThe application keeps running in the background.")
	exitText, _ := windows.UTF16PtrFromString("Exit the application\nStop downloads and the connection, then close safely.")
	buttons := []desktopTaskDialogButton{
		{ID: desktopCloseButtonHide, Text: hideText},
		{ID: desktopCloseButtonExit, Text: exitText},
	}
	config := desktopTaskDialogConfig{
		CbSize:          uint32(unsafe.Sizeof(desktopTaskDialogConfig{})),
		HWndParent:      windows.HWND(hwnd),
		Flags:           desktopTDFUseCommandLinks | desktopTDFAllowDialogCancellation | desktopTDFPositionRelative | desktopTDFSizeToContent,
		CommonButtons:   desktopTDCBFCancelButton,
		WindowTitle:     title,
		MainInstruction: instruction,
		Content:         content,
		ButtonCount:     uint32(len(buttons)),
		Buttons:         &buttons[0],
		DefaultButton:   desktopCloseButtonHide,
	}
	var selected int32
	if findErr := taskDialogIndirectProc.Find(); findErr == nil {
		result, _, _ := taskDialogIndirectProc.Call(
			uintptr(unsafe.Pointer(&config)),
			uintptr(unsafe.Pointer(&selected)),
			0,
			0,
		)
		runtime.KeepAlive(buttons)
		runtime.KeepAlive(config)
		if result == 0 {
			return desktopCloseChoiceFromButton(selected)
		}
	}

	fallbackText, _ := windows.UTF16PtrFromString("Choose Yes to hide the application in the system tray. Downloads will continue.\n\nChoose No to exit the application safely.\n\nChoose Cancel to return to the application.")
	installDesktopMessageBoxHook("Hide to tray", "Exit", "Cancel")
	result, _, _ := messageBoxWProc.Call(
		hwnd,
		uintptr(unsafe.Pointer(fallbackText)),
		uintptr(unsafe.Pointer(title)),
		desktopMBYesNoCancel|desktopMBIconQuestion|desktopMBTaskModal,
	)
	removeDesktopMessageBoxHook()
	return desktopCloseChoiceFromButton(int32(result))
}

func currentDesktopTraySnapshot() desktopTraySnapshot {
	snapshot := desktopTraySnapshot{
		Runtime: getGUIRuntimeState(),
		Visible: desktopWindowVisible.Load(),
	}
	if manager := currentDownloadManager(); manager != nil {
		_, queued, inFlight, _ := manager.stats()
		snapshot.Queued = queued
		snapshot.Active = inFlight - queued
		if snapshot.Active < 0 {
			snapshot.Active = 0
		}
	}
	return snapshot
}

func desktopTrayStatusLabel(snapshot desktopTraySnapshot) string {
	switch snapshot.Runtime {
	case "waiting":
		return "Status: waiting for Start"
	case "initializing", "starting":
		return "Status: starting"
	case "stopping":
		return "Status: stopping"
	case "running":
		if snapshot.Active > 0 || snapshot.Queued > 0 {
			return fmt.Sprintf("Status: running - %d active, %d queued", snapshot.Active, snapshot.Queued)
		}
		return "Status: running - no transfers"
	default:
		if snapshot.Runtime == "" {
			return "Status: unknown"
		}
		return "Status: " + snapshot.Runtime
	}
}

func desktopTrayToggleLabel(visible bool) string {
	if visible {
		return "Hide window"
	}
	return "Show window"
}

func promptDesktopExitConfirmation(hwnd uintptr, snapshot desktopTraySnapshot) bool {
	title, _ := windows.UTF16PtrFromString("Exit Discord Downloader Go?")
	message := desktopExitConfirmationMessage(snapshot)
	text, _ := windows.UTF16PtrFromString(message)
	installDesktopMessageBoxHook("Exit", "Cancel", "")
	result, _, _ := messageBoxWProc.Call(
		hwnd,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		desktopMBYesNo|desktopMBIconQuestion|desktopMBTaskModal,
	)
	removeDesktopMessageBoxHook()
	return int32(result) == desktopIDYes
}

func desktopExitConfirmationMessage(snapshot desktopTraySnapshot) string {
	if snapshot.Active > 0 || snapshot.Queued > 0 {
		return fmt.Sprintf(
			"There are still %d active and %d queued downloads. Exiting will safely stop the application and those transfers.",
			snapshot.Active,
			snapshot.Queued,
		)
	}
	return "The application, Discord connection, and download processes will be stopped safely."
}

func requestDesktopExit() {
	if !desktopGUIStopping.CompareAndSwap(false, true) {
		return
	}
	select {
	case loop <- os.Interrupt:
	default:
	}
}

func addDesktopTray() bool {
	if desktopWindowHandle == 0 || !shouldCloseGUIToTray() {
		return false
	}
	data := desktopNotifyData()
	result, _, _ := shellNotifyIconW.Call(desktopNIMAdd, uintptr(unsafe.Pointer(&data)))
	if result == 0 {
		return false
	}
	data.Version = desktopNotifyVersion4
	_, _, _ = shellNotifyIconW.Call(desktopNIMSetVersion, uintptr(unsafe.Pointer(&data)))
	desktopTrayAdded = true
	return true
}

func removeDesktopTray() {
	if !desktopTrayAdded || desktopWindowHandle == 0 {
		return
	}
	data := desktopNotifyData()
	_, _, _ = shellNotifyIconW.Call(desktopNIMDelete, uintptr(unsafe.Pointer(&data)))
	desktopTrayAdded = false
}

func showDesktopWindow() {
	if desktopWindowHandle == 0 {
		return
	}
	_, _, _ = showWindowProc.Call(desktopWindowHandle, desktopSWRestore)
	_, _, _ = setForegroundWindowProc.Call(desktopWindowHandle)
	desktopWindowVisible.Store(true)
}

func hideDesktopWindow(showNotice bool) {
	if desktopWindowHandle == 0 {
		return
	}
	_, _, _ = showWindowProc.Call(desktopWindowHandle, desktopSWHide)
	desktopWindowVisible.Store(false)
	if showNotice {
		showDesktopTrayNoticeOnce()
	}
}

func showDesktopTrayMenu() {
	menu, _, _ := createPopupMenuProc.Call()
	if menu == 0 {
		return
	}
	defer destroyMenuProc.Call(menu)
	snapshot := currentDesktopTraySnapshot()
	statusLabel, _ := windows.UTF16PtrFromString(desktopTrayStatusLabel(snapshot))
	toggleLabel, _ := windows.UTF16PtrFromString(desktopTrayToggleLabel(snapshot.Visible))
	exitLabel, _ := windows.UTF16PtrFromString("Exit the application...")
	_, _, _ = appendMenuWProc.Call(menu, desktopMFString|desktopMFGrayed, 0, uintptr(unsafe.Pointer(statusLabel)))
	_, _, _ = appendMenuWProc.Call(menu, desktopMFSeparator, 0, 0)
	_, _, _ = appendMenuWProc.Call(menu, desktopMFString, desktopMenuToggle, uintptr(unsafe.Pointer(toggleLabel)))
	_, _, _ = appendMenuWProc.Call(menu, desktopMFSeparator, 0, 0)
	_, _, _ = appendMenuWProc.Call(menu, desktopMFString, desktopMenuExit, uintptr(unsafe.Pointer(exitLabel)))
	point := desktopPoint{}
	_, _, _ = getCursorPosProc.Call(uintptr(unsafe.Pointer(&point)))
	_, _, _ = setForegroundWindowProc.Call(desktopWindowHandle)
	command, _, _ := trackPopupMenuProc.Call(menu, desktopTPMRightButton|desktopTPMReturnCmd,
		uintptr(point.X), uintptr(point.Y), 0, desktopWindowHandle, 0)
	switch command {
	case desktopMenuToggle:
		if snapshot.Visible {
			hideDesktopWindow(true)
		} else {
			showDesktopWindow()
		}
	case desktopMenuExit:
		if promptDesktopExitConfirmation(desktopWindowHandle, currentDesktopTraySnapshot()) {
			requestDesktopExit()
		}
	}
}

func desktopWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	if message == desktopWMClose && shouldCloseGUIToTray() && desktopTrayAdded && !desktopGUIStopping.Load() {
		switch promptDesktopCloseChoice(hwnd) {
		case desktopCloseHide:
			hideDesktopWindow(true)
		case desktopCloseExit:
			requestDesktopExit()
		}
		return 0
	}
	if message == desktopTrayMessage {
		event := uint32(lParam & 0xffff)
		switch event {
		case desktopWMLButtonUp, desktopWMLButtonDblClk, desktopNINSelect, desktopNINKeySelect:
			showDesktopWindow()
		case desktopWMRButtonUp, desktopWMContextMenu:
			showDesktopTrayMenu()
		}
		return 0
	}
	if desktopTaskbarCreated != 0 && message == desktopTaskbarCreated {
		desktopTrayAdded = false
		addDesktopTray()
		return 0
	}
	if desktopOldWindowProc != 0 {
		result, _, _ := callWindowProcW.Call(desktopOldWindowProc, hwnd, uintptr(message), wParam, lParam)
		return result
	}
	return 0
}

func enableDesktopHighDPI() {
	// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is the signed pointer value -4.
	// Calling this before the first HWND prevents Windows from bitmap-scaling the
	// WebView, which makes text look blurry at 125%, 150%, and mixed-monitor DPI.
	_, _, _ = setProcessDPIAware.Call(^uintptr(3))
}

func launchDesktopGUI(url string) bool {
	ready := make(chan bool, 1)
	desktopGUIStopping.Store(false)
	desktopTrayNoticeSeen.Store(false)
	desktopWindowVisible.Store(false)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		enableDesktopHighDPI()

		window := webview2.NewWithOptions(webview2.WebViewOptions{
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title:  "Discord Downloader Go",
				Width:  1440,
				Height: 900,
				Center: true,
			},
		})
		if window == nil {
			ready <- false
			return
		}
		desktopGUIMu.Lock()
		desktopGUIWindow = window
		desktopGUIMu.Unlock()
		window.SetSize(980, 680, webview2.HintMin)
		desktopWindowHandle = uintptr(window.Window())
		desktopWindowVisible.Store(true)
		desktopOldWindowProc, _, _ = setWindowLongPtrW.Call(desktopWindowHandle, desktopGWLWndProc, desktopWindowProcPointer)
		taskbarMessage, _ := windows.UTF16PtrFromString("TaskbarCreated")
		registered, _, _ := registerWindowMessageW.Call(uintptr(unsafe.Pointer(taskbarMessage)))
		desktopTaskbarCreated = uint32(registered)
		if shouldCloseGUIToTray() && !addDesktopTray() {
			log.Println(lg("GUI", "Tray", color.YellowString, "Unable to create the Windows tray icon; closing the window will exit"))
		}
		window.Navigate(url)
		if shouldStartGUIMinimized() && desktopTrayAdded {
			hideDesktopWindow(true)
		}
		ready <- true
		window.Run()

		removeDesktopTray()
		desktopGUIMu.Lock()
		desktopGUIWindow = nil
		desktopWindowHandle = 0
		desktopOldWindowProc = 0
		desktopWindowVisible.Store(false)
		desktopGUIMu.Unlock()
		if !desktopGUIStopping.Load() {
			select {
			case loop <- os.Interrupt:
			default:
			}
		}
	}()
	return <-ready
}

func shutdownDesktopGUI() {
	desktopGUIStopping.Store(true)
	desktopGUIMu.Lock()
	window := desktopGUIWindow
	desktopGUIMu.Unlock()
	if window != nil {
		window.Dispatch(func() {
			removeDesktopTray()
			if desktopWindowHandle != 0 && desktopOldWindowProc != 0 {
				_, _, _ = setWindowLongPtrW.Call(desktopWindowHandle, desktopGWLWndProc, desktopOldWindowProc)
			}
			window.Destroy()
		})
	}
}
