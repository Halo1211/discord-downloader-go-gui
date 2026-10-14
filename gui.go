package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
)

const guiRequestBodyLimit int64 = 16 * 1024

//go:embed assets/discord-downloader-go-logo.svg
var guiLogoSVG string

var (
	guiFlagEnabled     bool
	guiFlagDisabled    bool
	guiFlagAddress     string
	guiFlagOpen        bool
	guiFlagOpenWasSet  bool
	guiFlagBrowser     bool
	guiFlagAutoStart   bool
	guiFlagNoTray      bool
	guiFlagMinimized   bool
	guiServer          *http.Server
	guiServerMu        sync.Mutex
	guiToken           string
	guiURL             string
	guiStartupIssue    string
	guiStartupIssueMu  sync.RWMutex
	guiRuntimeState    = "initializing"
	guiRuntimeStateMu  sync.RWMutex
	guiRuntimeStart    = make(chan struct{}, 1)
	guiShutdownTimeout = 5 * time.Second
)

func setGUIStartupIssue(issue string) {
	guiStartupIssueMu.Lock()
	guiStartupIssue = strings.TrimSpace(issue)
	guiStartupIssueMu.Unlock()
}

func getGUIStartupIssue() string {
	guiStartupIssueMu.RLock()
	defer guiStartupIssueMu.RUnlock()
	return guiStartupIssue
}

func setGUIRuntimeState(state string) {
	guiRuntimeStateMu.Lock()
	guiRuntimeState = state
	guiRuntimeStateMu.Unlock()
}

func getGUIRuntimeState() string {
	guiRuntimeStateMu.RLock()
	defer guiRuntimeStateMu.RUnlock()
	return guiRuntimeState
}

func requestGUIRuntimeStart() bool {
	guiRuntimeStateMu.Lock()
	defer guiRuntimeStateMu.Unlock()
	if guiRuntimeState != "waiting" {
		return false
	}
	select {
	case guiRuntimeStart <- struct{}{}:
		guiRuntimeState = "starting"
		return true
	default:
		return false
	}
}

func parseRuntimeArgs(args []string) {
	positionalConfigSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--gui":
			guiFlagEnabled = true
		case arg == "--no-gui":
			guiFlagDisabled = true
		case arg == "--gui-open":
			guiFlagOpen = true
			guiFlagOpenWasSet = true
		case arg == "--no-gui-open":
			guiFlagOpen = false
			guiFlagOpenWasSet = true
		case arg == "--gui-browser":
			guiFlagBrowser = true
		case arg == "--gui-auto-start":
			guiFlagAutoStart = true
		case arg == "--no-tray":
			guiFlagNoTray = true
		case arg == "--start-minimized":
			guiFlagMinimized = true
		case arg == "--gui-address" && i+1 < len(args):
			i++
			guiFlagAddress = args[i]
		case strings.HasPrefix(arg, "--gui-address="):
			guiFlagAddress = strings.TrimPrefix(arg, "--gui-address=")
		case arg == "--config" && i+1 < len(args):
			i++
			configFileBase = args[i]
			positionalConfigSet = true
		case strings.HasPrefix(arg, "--config="):
			configFileBase = strings.TrimPrefix(arg, "--config=")
			positionalConfigSet = true
		case !strings.HasPrefix(arg, "-") && !positionalConfigSet:
			// Preserve the original positional settings-base argument.
			configFileBase = arg
			positionalConfigSet = true
		}
	}
}

func shouldStartGUI() bool {
	if guiFlagDisabled {
		return false
	}
	return guiFlagEnabled || config.GUIEnabled
}

func effectiveGUIAddress() string {
	if strings.TrimSpace(guiFlagAddress) != "" {
		return strings.TrimSpace(guiFlagAddress)
	}
	return strings.TrimSpace(config.GUIAddress)
}

func validateLocalGUIAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return errors.New("GUI address cannot be empty")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("GUI address must use host:port format: %w", err)
	}
	for _, digit := range portText {
		if digit < '0' || digit > '9' {
			return errors.New("GUI address must use a numeric port between 0 and 65535")
		}
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("GUI address must use a numeric port between 0 and 65535")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("GUI address must remain on localhost to protect Discord credentials")
	}
	return nil
}

func guiRequestHostAllowed(hostPort string) bool {
	host := strings.TrimSpace(hostPort)
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	} else {
		host = strings.Trim(host, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func shouldOpenGUIBrowser() bool {
	if guiFlagOpenWasSet {
		return guiFlagOpen
	}
	return config.GUIOpenBrowser
}

func shouldCloseGUIToTray() bool {
	return !guiFlagNoTray && config.GUICloseToTray
}

func shouldStartGUIMinimized() bool {
	return guiFlagMinimized || config.GUIStartMinimized
}

func shouldAutoStartGUIRuntime() bool {
	return guiFlagAutoStart || config.GUIAutoStart
}

func startGUI() error {
	guiServerMu.Lock()
	defer guiServerMu.Unlock()
	if guiServer != nil {
		return nil
	}

	address := effectiveGUIAddress()
	if err := validateLocalGUIAddress(address); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		listener.Close()
		return err
	}
	guiToken = base64.RawURLEncoding.EncodeToString(tokenBytes)
	guiURL = browserURLForListener(listener)

	mux := http.NewServeMux()
	mux.HandleFunc("/", guiPageHandler)
	mux.HandleFunc("/api/status", guiStatusHandler)
	mux.HandleFunc("/api/logs", guiLogsHandler)
	mux.HandleFunc("/api/config", guiConfigHandler)
	mux.HandleFunc("/api/runtime/start", guiStartRuntimeHandler)
	mux.HandleFunc("/api/history/cancel", guiCancelHistoryHandler)
	guiServer = &http.Server{
		Handler:           guiSecurityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func(server *http.Server) {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println(lg("GUI", "Server", color.HiRedString, "GUI server stopped unexpectedly: %s", err))
		}
	}(guiServer)
	log.Println(lg("GUI", "Startup", color.HiGreenString, "Dashboard available at %s", guiURL))
	if shouldOpenGUIBrowser() {
		if guiFlagBrowser || !launchDesktopGUI(guiURL) {
			log.Println(lg("GUI", "Desktop", color.YellowString, "Native window unavailable; using the system browser"))
			go openSystemBrowser(guiURL)
		}
	}
	return nil
}

func browserURLForListener(listener net.Listener) string {
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "http://127.0.0.1:7744"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func openSystemBrowser(url string) {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		command = exec.Command("open", url)
	default:
		command = exec.Command("xdg-open", url)
	}
	if err := command.Start(); err != nil {
		log.Println(lg("GUI", "Browser", color.YellowString, "Open %s manually (%s)", url, err))
	}
}

func shutdownGUI() {
	shutdownDesktopGUI()
	guiServerMu.Lock()
	server := guiServer
	guiServer = nil
	guiServerMu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), guiShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Println(lg("GUI", "Shutdown", color.YellowString, "GUI shutdown error: %s", err))
	}
}

func guiSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if !guiRequestHostAllowed(r.Host) {
			guiWriteError(w, http.StatusForbidden, "GUI requests must use a localhost address")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func guiPageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		guiMethodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := strings.ReplaceAll(guiHTML, "{{GUI_TOKEN}}", template.HTMLEscapeString(guiToken))
	page = strings.ReplaceAll(page, "{{GUI_LOGO}}", guiLogoSVG)
	_, _ = io.WriteString(w, page)
}

type guiHistoryJobView struct {
	ChannelID     string `json:"channelId"`
	Status        string `json:"status"`
	Origin        string `json:"origin"`
	Before        string `json:"before,omitempty"`
	Since         string `json:"since,omitempty"`
	DownloadCount int64  `json:"downloadCount"`
	DownloadSize  int64  `json:"downloadSize"`
	Added         string `json:"added"`
	Updated       string `json:"updated"`
}

func guiStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		guiMethodNotAllowed(w, http.MethodGet)
		return
	}
	// Manual Start reloads settings while the dashboard continues polling.
	configFileMu.Lock()
	defer configFileMu.Unlock()
	workers, queued, inFlight, queueStopped := 0, 0, 0, true
	if manager := currentDownloadManager(); manager != nil {
		workers, queued, inFlight, queueStopped = manager.stats()
	}
	historyViews := make([]guiHistoryJobView, 0)
	waiting, running, completed, failed := 0, 0, 0, 0
	if historyJobs != nil {
		for _, pair := range historyJobs.Snapshot() {
			job := pair.Value
			switch {
			case job.Status == historyStatusWaiting:
				waiting++
			case job.Status == historyStatusRunning || job.Status == historyStatusAbortRequested:
				running++
			case job.Status == historyStatusErrorReadMessageHistoryPerms || job.Status == historyStatusErrorRequesting || job.Status == historyStatusErrorDownloading:
				failed++
			case job.Status >= historyStatusAbortCompleted:
				completed++
			}
			historyViews = append(historyViews, guiHistoryJobView{
				ChannelID:     pair.Key,
				Status:        historyStatusLabel(job.Status),
				Origin:        job.OriginUser,
				Before:        job.TargetBefore,
				Since:         job.TargetSince,
				DownloadCount: job.DownloadCount,
				DownloadSize:  job.DownloadSize,
				Added:         guiTime(job.Added),
				Updated:       guiTime(job.Updated),
			})
		}
	}

	guildCount := 0
	session := currentDiscordSession()
	ready := session != nil && botReady.Load()
	if session != nil && session.State != nil {
		session.State.RLock()
		guildCount = len(session.State.Guilds)
		session.State.RUnlock()
	}
	_, lastDownload, lastMessage := runtimeTelemetrySnapshot()
	downloadCount := dbCachedDownloadCount()

	guiWriteJSON(w, http.StatusOK, map[string]interface{}{
		"version":       projectVersion,
		"startupIssue":  getGUIStartupIssue(),
		"uptimeSeconds": int64(time.Since(startTime).Seconds()),
		"runtime": map[string]interface{}{
			"state": getGUIRuntimeState(),
		},
		"bot": map[string]interface{}{
			"ready":      ready,
			"guildCount": guildCount,
			"username": func() string {
				if user := currentDiscordUser(); user != nil {
					return user.Username
				}
				return "Starting"
			}(),
		},
		"downloads": map[string]interface{}{
			"total":        downloadCount,
			"workers":      workers,
			"queued":       queued,
			"inFlight":     inFlight,
			"queueStopped": queueStopped,
			"lastDownload": guiTime(lastDownload),
			"lastMessage":  guiTime(lastMessage),
		},
		"history": map[string]interface{}{
			"waiting":   waiting,
			"running":   running,
			"completed": completed,
			"failed":    failed,
			"jobs":      historyViews,
		},
		"config": map[string]interface{}{
			"file":             configFile,
			"watchSettings":    config.WatchSettings,
			"downloadTimeout":  config.DownloadTimeout,
			"downloadRetryMax": config.DownloadRetryMax,
			"maxFileSize":      config.DownloadMaxFileSize,
			"historyMaxJobs":   config.HistoryMaxJobs,
			"boundChannels":    getBoundChannelsCount(),
			"boundCategories":  getBoundCategoriesCount(),
			"boundServers":     getBoundServersCount(),
			"boundUsers":       getBoundUsersCount(),
		},
	})
}

func guiStartRuntimeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		guiMethodNotAllowed(w, http.MethodPost)
		return
	}
	if !guiAuthorized(r) {
		guiWriteError(w, http.StatusForbidden, "invalid GUI request token")
		return
	}
	configFileMu.Lock()
	candidate, err := guiReadConfiguration()
	configFileMu.Unlock()
	if err != nil {
		setGUIStartupIssue(err.Error())
		guiWriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := validateRuntimeCredentials(candidate); err != nil {
		setGUIStartupIssue(err.Error())
		guiWriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if !requestGUIRuntimeStart() {
		guiWriteError(w, http.StatusConflict, "runtime is not waiting to start")
		return
	}
	guiWriteJSON(w, http.StatusAccepted, map[string]interface{}{
		"ok":      true,
		"message": "runtime start requested",
	})
}

type guiHistoryRequest struct {
	ChannelID string `json:"channelId"`
}

func guiCancelHistoryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		guiMethodNotAllowed(w, http.MethodPost)
		return
	}
	if !guiAuthorized(r) {
		guiWriteError(w, http.StatusForbidden, "invalid GUI request token")
		return
	}
	var request guiHistoryRequest
	if err := guiDecodeJSON(r, &request); err != nil {
		guiWriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	channelID := strings.TrimSpace(request.ChannelID)
	if historyJobs == nil {
		guiWriteError(w, http.StatusNotFound, "history job was not found")
		return
	}
	found, changed := historyJobs.RequestCancel(channelID)
	if !found {
		guiWriteError(w, http.StatusNotFound, "history job was not found")
		return
	}
	if !changed {
		guiWriteError(w, http.StatusConflict, "history job is not active")
		return
	}
	guiWriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "cancellation requested"})
}

func guiAuthorized(r *http.Request) bool {
	if r == nil || guiToken == "" {
		return false
	}
	provided := r.Header.Get("X-DDG-GUI-Token")
	return len(provided) == len(guiToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(guiToken)) == 1
}

func guiDecodeJSON(r *http.Request, target interface{}) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, guiRequestBodyLimit+1))
	if err != nil {
		return fmt.Errorf("invalid request: failed to read body: %w", err)
	}
	if int64(len(body)) > guiRequestBodyLimit {
		return fmt.Errorf("invalid request: body exceeds %d bytes", guiRequestBodyLimit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("invalid request: multiple JSON values are not allowed")
		}
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func guiMethodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	guiWriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func guiWriteError(w http.ResponseWriter, status int, message string) {
	guiWriteJSON(w, status, map[string]interface{}{"ok": false, "error": message})
}

func guiWriteJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func guiTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}

const guiHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <meta name="ddg-token" content="{{GUI_TOKEN}}">
  <title>Discord Archive Console</title>
  <style>
    :root { color-scheme: dark; --bg:#07111f; --panel:#0d1b2d; --panel2:#12243a; --line:#203650; --text:#eef6ff; --muted:#8da5be; --cyan:#4de1c1; --blue:#6ca8ff; --amber:#ffc66d; --red:#ff7c91; }
    * { box-sizing:border-box; }
    body { margin:0; min-height:100vh; background:radial-gradient(circle at 10% 0,#123257 0,transparent 35%),linear-gradient(150deg,var(--bg),#081725 60%,#06101b); color:var(--text); font:14px/1.45 Inter,Segoe UI,system-ui,sans-serif; }
    .shell { width:min(1180px,calc(100% - 32px)); margin:0 auto; padding:32px 0 56px; }
    header { display:flex; align-items:center; justify-content:space-between; gap:20px; margin-bottom:26px; }
    .brand { display:flex; align-items:center; gap:14px; }
    .mark { width:46px; height:46px; display:grid; place-items:center; border:1px solid #397090; border-radius:14px; background:linear-gradient(145deg,#1b4666,#10283d); color:var(--cyan); font-size:21px; font-weight:900; box-shadow:0 14px 36px #0007; }
    h1 { margin:0; font-size:22px; letter-spacing:.2px; } .subtitle { color:var(--muted); margin-top:3px; }
    .pill { display:flex; align-items:center; gap:8px; padding:9px 13px; border:1px solid var(--line); border-radius:999px; background:#0b1929cc; color:var(--muted); }
    .dot { width:8px; height:8px; border-radius:50%; background:var(--amber); box-shadow:0 0 14px currentColor; } .dot.ok { background:var(--cyan); }
    .grid { display:grid; grid-template-columns:repeat(4,1fr); gap:14px; margin-bottom:14px; }
    .card,.panel { border:1px solid var(--line); background:linear-gradient(145deg,#10243aee,#0a1828ee); border-radius:18px; box-shadow:0 18px 45px #0004; }
    .card { padding:18px; min-height:118px; } .label { color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:1.1px; }
    .value { margin:10px 0 2px; font-size:28px; font-weight:750; } .hint { color:var(--muted); font-size:12px; }
    .layout { display:grid; grid-template-columns:minmax(0,1.5fr) minmax(280px,.75fr); gap:14px; }
    .panel { padding:20px; margin-bottom:14px; } .panel-head { display:flex; justify-content:space-between; align-items:center; gap:14px; margin-bottom:16px; }
    h2 { font-size:15px; margin:0; letter-spacing:.3px; } button { border:0; border-radius:10px; padding:10px 14px; cursor:pointer; font-weight:700; color:#04131b; background:var(--cyan); }
    button.secondary { color:var(--text); background:#17314a; border:1px solid #2d4d6b; } button.danger { color:#ffeef1; background:#542438; border:1px solid #8c3a52; padding:7px 10px; }
    button:disabled { opacity:.5; cursor:not-allowed; }
    form { display:grid; grid-template-columns:1.4fr 1fr 1fr auto; gap:10px; }
    label span { display:block; color:var(--muted); font-size:11px; margin:0 0 5px 2px; } input { width:100%; height:40px; border:1px solid #28445f; border-radius:10px; padding:0 11px; color:var(--text); background:#071522; outline:none; }
    input:focus { border-color:var(--cyan); box-shadow:0 0 0 3px #4de1c11b; } form button { align-self:end; height:40px; }
    .table-wrap { overflow:auto; } table { width:100%; border-collapse:collapse; min-width:620px; } th,td { padding:11px 9px; text-align:left; border-bottom:1px solid #1d344b; } th { color:var(--muted); font-size:11px; text-transform:uppercase; letter-spacing:.7px; } td { font-size:13px; }
    .status { display:inline-flex; padding:4px 8px; border-radius:999px; background:#183149; color:var(--blue); } .status.failed { background:#482336; color:#ff9bad; }
    .empty { color:var(--muted); padding:26px 6px; text-align:center; }
    .config { display:grid; grid-template-columns:1fr auto; gap:11px; } .config div:nth-child(odd) { color:var(--muted); } .config div:nth-child(even) { text-align:right; font-weight:650; overflow-wrap:anywhere; }
    .settings-panel { margin-top:14px; } .settings-actions { display:flex; align-items:center; gap:9px; flex-wrap:wrap; }
    .settings-actions input { width:min(330px,70vw); } .settings-editor { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; max-height:72vh; overflow:auto; padding:2px 5px 4px 1px; }
    .setting-field { min-width:0; padding:11px 12px; border:1px solid #203b55; border-radius:12px; background:#091827; }
    .setting-field.wide { grid-column:1/-1; } .setting-field > label { display:block; color:var(--muted); font-size:11px; letter-spacing:.45px; margin-bottom:6px; }
    .setting-field input[type="checkbox"] { width:19px; height:19px; accent-color:var(--cyan); } .setting-field textarea { width:100%; min-height:92px; resize:vertical; border:1px solid #28445f; border-radius:10px; padding:10px 11px; color:var(--text); background:#071522; font:12px/1.5 ui-monospace,SFMono-Regular,Consolas,monospace; outline:none; }
    .setting-field textarea:focus { border-color:var(--cyan); box-shadow:0 0 0 3px #4de1c11b; } .setting-object { grid-column:1/-1; border:1px solid #28445f; border-radius:12px; background:#081625; }
    .setting-object summary { cursor:pointer; padding:12px 14px; font-weight:700; color:var(--blue); } .setting-object-body { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:9px; padding:0 10px 10px; }
    .settings-note { color:var(--muted); font-size:12px; margin:-5px 0 14px; } .settings-empty { color:var(--muted); padding:24px; text-align:center; grid-column:1/-1; }
    .setup { display:none; margin-bottom:18px; padding:14px 16px; border:1px solid #8a6232; border-radius:14px; background:#332719; color:#ffdca8; line-height:1.5; }
    .setup.show { display:block; }
    .toast { position:fixed; right:22px; bottom:22px; max-width:360px; padding:13px 16px; border:1px solid #35647d; border-radius:12px; background:#102a3a; box-shadow:0 18px 50px #0008; transform:translateY(90px); opacity:0; transition:.22s ease; }
    .toast.show { transform:none; opacity:1; } .toast.error { border-color:#7f3850; background:#351b29; }
    @media (max-width:900px) { .grid { grid-template-columns:repeat(2,1fr); } .layout { grid-template-columns:1fr; } }
    @media (max-width:620px) { .shell { width:min(100% - 20px,1180px); padding-top:20px; } header { align-items:flex-start; } .grid { grid-template-columns:1fr 1fr; } form { grid-template-columns:1fr 1fr; } form label:first-child { grid-column:1/-1; } form button { width:100%; } .value { font-size:23px; } .settings-editor,.setting-object-body { grid-template-columns:1fr; } }

    /* Relay cockpit: an original interface for the Discord archive workflow. */
    :root { --bg:#050806; --surface:#0b100d; --surface2:#101813; --surface3:#151f18; --line:#263329; --line2:#34463a; --text:#f4f2e9; --muted:#8d9b91; --lime:#b7f36b; --mint:#49d7a4; --amber:#ffbe64; --coral:#ff7267; --blue:#8eb5ff; }
    html { scroll-behavior:smooth; }
    body { min-width:320px; background:radial-gradient(circle at 74% -20%,#183524 0,transparent 38%),linear-gradient(145deg,#050806 0,#080d09 52%,#050706 100%); color:var(--text); font:14px/1.45 Inter,"Segoe UI",system-ui,sans-serif; -webkit-font-smoothing:antialiased; text-rendering:optimizeLegibility; }
    body:before { content:""; position:fixed; inset:0; pointer-events:none; opacity:.18; background-image:linear-gradient(#fff 1px,transparent 1px),linear-gradient(90deg,#fff 1px,transparent 1px); background-size:48px 48px; mask-image:linear-gradient(to bottom,#000,transparent 62%); }
    button,input,textarea { font:inherit; }
    button { min-height:40px; border:1px solid transparent; border-radius:9px; padding:9px 14px; color:#10160f; background:var(--lime); transition:border-color .18s ease,background .18s ease,transform .18s ease,color .18s ease; }
    button:hover:not(:disabled) { transform:translateY(-1px); background:#cafc8b; }
    button:focus-visible,input:focus-visible,textarea:focus-visible,summary:focus-visible { outline:2px solid var(--lime); outline-offset:2px; }
    button.secondary { color:var(--text); background:#121a15; border-color:var(--line2); }
    button.secondary:hover:not(:disabled),button.secondary.active { color:var(--lime); background:#18241b; border-color:#58734f; }
    button.danger { min-height:32px; color:#ffd9d5; background:#2a1715; border-color:#6b3731; padding:5px 10px; }
    .app-shell { position:relative; width:min(1500px,calc(100% - 36px)); margin:0 auto; padding:18px 0 44px; }
    .topbar { min-height:64px; display:flex; align-items:center; justify-content:space-between; gap:22px; margin:0 0 14px; padding:0 4px; }
    .brand-lockup { display:flex; align-items:center; gap:13px; min-width:0; }
    .brand-mark { position:relative; width:48px; height:48px; flex:0 0 auto; display:grid; place-items:center; padding:4px; border:1px solid #47604e; border-radius:15px; background:#0e1711; box-shadow:0 0 0 5px #0d160f,0 0 32px #71d25c22; }
    .brand-mark svg { display:block; width:100%; height:100%; }
    .brand-name { font-size:17px; font-weight:760; letter-spacing:.02em; white-space:nowrap; }
    .brand-name em { color:var(--lime); font-style:normal; }
    .brand-caption,.microcopy { color:var(--muted); font:10px/1.4 ui-monospace,Consolas,monospace; letter-spacing:.14em; text-transform:uppercase; }
    .topbar-actions { display:flex; align-items:center; justify-content:flex-end; gap:8px; flex-wrap:wrap; }
    .pill { min-height:34px; padding:7px 11px; color:var(--muted); background:#0b100dcc; border-color:var(--line); font:10px/1 ui-monospace,Consolas,monospace; letter-spacing:.08em; text-transform:uppercase; }
    .pill strong { color:var(--text); font-weight:650; }
    .dot { width:7px; height:7px; background:var(--amber); }
    .dot.ok { background:var(--mint); }
    .setup { margin:0 0 14px; border-color:#775d2d; border-radius:10px; background:#251d0f; color:#ffd895; }
    .workspace { display:grid; grid-template-columns:330px minmax(0,1fr); gap:12px; align-items:start; }
    .sidebar { display:grid; gap:12px; position:sticky; top:12px; max-height:calc(100vh - 24px); overflow-y:auto; padding-right:4px; scrollbar-width:thin; scrollbar-color:var(--line2) transparent; }
    .sidebar::-webkit-scrollbar { width:6px; } .sidebar::-webkit-scrollbar-thumb { border-radius:999px; background:var(--line2); }
    .content { min-width:0; }
    .panel,.telemetry,.side-card { border:1px solid var(--line); border-radius:12px; background:linear-gradient(145deg,#0e1510f2,#090e0bf2); box-shadow:0 18px 50px #0005; }
    .side-card { padding:17px; }
    .side-card h2,.panel h2 { font:700 11px/1.2 ui-monospace,Consolas,monospace; letter-spacing:.14em; text-transform:uppercase; }
    .panel-kicker { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:13px; color:var(--muted); font:10px/1.2 ui-monospace,Consolas,monospace; letter-spacing:.12em; text-transform:uppercase; }
    .panel-kicker:after { content:""; height:1px; flex:1; background:linear-gradient(90deg,var(--line2),transparent); }
    label span { color:#a8b2aa; font:9px/1.2 ui-monospace,Consolas,monospace; letter-spacing:.12em; }
    input { height:42px; border-color:var(--line2); border-radius:8px; color:var(--text); background:#070b08; }
    input::placeholder,textarea::placeholder { color:#58635b; }
    input:focus { border-color:#729759; box-shadow:0 0 0 3px #b7f36b14; }
    .scope-list { display:grid; grid-template-columns:1fr auto; gap:9px 12px; }
    .scope-list div:nth-child(odd) { color:var(--muted); }
    .scope-list div:nth-child(even) { text-align:right; color:var(--text); font:600 11px/1.4 ui-monospace,Consolas,monospace; overflow-wrap:anywhere; }
    .side-actions { display:grid; grid-template-columns:1fr 1fr; gap:8px; margin-top:14px; }
    .runtime-control { display:grid; gap:8px; margin:12px 0 4px; padding:11px; border:1px solid var(--line); border-radius:9px; background:#080d0a; }
    .runtime-state { display:flex; align-items:center; justify-content:space-between; gap:10px; color:var(--muted); font:650 9px/1.3 ui-monospace,Consolas,monospace; letter-spacing:.08em; text-transform:uppercase; }
    .runtime-state strong { color:var(--amber); }
    .runtime-state strong.running { color:var(--mint); }
    .runtime-start { width:100%; }
    .health-copy { min-height:42px; color:#b9c2bb; font-size:12px; }
    .telemetry { position:relative; overflow:hidden; display:grid; grid-template-columns:minmax(230px,1.15fr) repeat(3,minmax(120px,.55fr)); min-height:205px; margin-bottom:12px; }
    .telemetry:after { content:""; position:absolute; inset:auto 0 0; height:3px; background:linear-gradient(90deg,var(--lime),var(--mint) 55%,transparent); }
    .hero-stat { padding:24px; border-right:1px solid var(--line); }
    .hero-number { margin:10px 0 4px; color:var(--lime); font:750 clamp(42px,5.4vw,74px)/.95 "Segoe UI",system-ui,sans-serif; letter-spacing:-.055em; }
    .hero-number small { margin-left:6px; color:var(--muted); font:600 10px/1 ui-monospace,Consolas,monospace; letter-spacing:.08em; text-transform:uppercase; }
    .telemetry-stat { display:flex; flex-direction:column; justify-content:center; padding:20px; border-right:1px solid var(--line); }
    .telemetry-stat:last-child { border-right:0; }
    .telemetry-value { margin:10px 0 5px; font-size:27px; font-weight:720; letter-spacing:-.04em; }
    .telemetry-note { color:var(--muted); font-size:11px; }
    .signal { position:absolute; left:24px; right:24px; bottom:20px; height:32px; display:flex; align-items:flex-end; gap:4px; opacity:.72; }
    .signal span { flex:1; min-width:2px; height:16%; border-radius:2px 2px 0 0; background:linear-gradient(to top,#263d2b,var(--mint)); transition:height .35s ease; }
    .panel { padding:0; margin:0 0 12px; overflow:hidden; }
    .panel-head { min-height:63px; margin:0; padding:13px 16px; border-bottom:1px solid var(--line); }
    .panel-title { display:flex; align-items:center; gap:10px; min-width:0; }
    .count-chip { min-width:25px; padding:4px 7px; border:1px solid #4d684f; border-radius:999px; color:var(--lime); background:#111b13; text-align:center; font:700 10px/1 ui-monospace,Consolas,monospace; }
    .activity-tools,.settings-actions { display:flex; align-items:center; gap:8px; flex-wrap:wrap; justify-content:flex-end; }
    .activity-tools input { width:190px; height:36px; }
    .activity-tools button,.settings-actions button { min-height:36px; padding:7px 11px; }
    .job-tabs { display:flex; gap:4px; padding:9px 15px; border-bottom:1px solid var(--line); overflow:auto; }
    .job-tabs button { min-height:31px; padding:5px 10px; border-color:transparent; color:var(--muted); background:transparent; font:650 10px/1 ui-monospace,Consolas,monospace; text-transform:uppercase; letter-spacing:.07em; }
    .job-tabs button:hover:not(:disabled),.job-tabs button.active { transform:none; color:var(--lime); background:#152019; border-color:var(--line2); }
    .table-wrap { max-height:430px; overflow:auto; }
    table { min-width:680px; }
    th,td { padding:12px 16px; border-color:#1c271f; }
    th { position:sticky; top:0; z-index:1; color:#77867b; background:#0b100d; font:650 9px/1 ui-monospace,Consolas,monospace; letter-spacing:.12em; }
    td { font-size:12px; }
    .status { border:1px solid #344c38; border-radius:6px; color:var(--mint); background:#102017; font:650 9px/1.2 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .status.failed { border-color:#66362f; color:#ff9a91; background:#291613; }
    .empty { padding:38px 10px; }
    .settings-panel { margin-top:0; }
    .settings-panel .panel-head { align-items:center; }
    .settings-actions input { width:min(280px,48vw); height:36px; }
    .settings-mode { display:flex; align-items:center; gap:3px; padding:3px; border:1px solid var(--line); border-radius:9px; background:#070b08; }
    .settings-mode button { min-height:28px; padding:4px 9px; border:0; color:var(--muted); background:transparent; font:700 9px/1 ui-monospace,Consolas,monospace; letter-spacing:.06em; text-transform:uppercase; }
    .settings-mode button:hover:not(:disabled),.settings-mode button.active { transform:none; color:#0c140c; background:var(--lime); }
    .settings-mode-note { padding:9px 16px; border-bottom:1px solid var(--line); color:var(--muted); background:#0a0f0c; font-size:11px; }
    .settings-mode-note strong { color:var(--lime); }
    .settings-note { margin:0; padding:11px 16px; border-bottom:1px solid var(--line); color:var(--muted); background:#0a0f0c; }
    .settings-editor { max-height:68vh; grid-template-columns:repeat(3,minmax(0,1fr)); gap:8px; padding:12px; }
    .setting-field { border-color:var(--line); border-radius:9px; background:#0b120d; }
    .setting-field textarea { border-color:var(--line2); border-radius:7px; color:var(--text); background:#060a07; }
    .setting-object { border-color:var(--line2); border-radius:9px; background:#080d0a; }
    .setting-object summary { color:var(--lime); }
    .setting-object-body { grid-template-columns:repeat(3,minmax(0,1fr)); }
    .toast { z-index:10; border-color:#4d7256; border-radius:9px; color:var(--text); background:#132219; }
    .toast.error { border-color:#7f443c; background:#301816; }
    .screenreader { position:absolute; width:1px; height:1px; overflow:hidden; clip:rect(0,0,0,0); white-space:nowrap; }
    @media (max-width:1100px) { .workspace { grid-template-columns:290px minmax(0,1fr); } .telemetry { grid-template-columns:1.1fr repeat(3,.6fr); } .hero-stat { padding:20px; } .telemetry-stat { padding:16px; } .settings-editor,.setting-object-body { grid-template-columns:repeat(2,minmax(0,1fr)); } }
    @media (max-width:820px) { .app-shell { width:min(100% - 22px,1500px); } .workspace { grid-template-columns:1fr; } .sidebar { position:static; grid-template-columns:1fr 1fr; max-height:none; overflow:visible; padding-right:0; } .telemetry { grid-template-columns:1fr 1fr; } .hero-stat { grid-column:1/-1; border-right:0; border-bottom:1px solid var(--line); min-height:160px; } .telemetry-stat:nth-of-type(3) { border-right:0; } .signal { right:24px; max-width:48%; margin-left:auto; } }
    @media (max-width:580px) { .app-shell { width:min(100% - 14px,1500px); padding-top:9px; } .topbar { align-items:flex-start; } .brand-caption,.topbar .pill:first-child { display:none; } .topbar-actions { gap:5px; } .pill { padding:6px 8px; } .sidebar { grid-template-columns:1fr; } .telemetry { grid-template-columns:1fr 1fr; } .telemetry-stat { min-height:108px; } .telemetry-stat:last-child { grid-column:1/-1; border-top:1px solid var(--line); } .signal { display:none; } .panel-head { align-items:flex-start; flex-direction:column; } .activity-tools,.settings-actions { width:100%; justify-content:stretch; } .activity-tools input,.settings-actions input { width:100%; flex:1 1 100%; } .activity-tools button,.settings-actions button { flex:1; } .settings-editor,.setting-object-body { grid-template-columns:1fr; } }

    /* Guided configuration center */
    .settings-hub { overflow:visible; }
    .settings-hub-head { position:sticky; top:0; z-index:5; border-radius:12px 12px 0 0; background:#0c120eee; backdrop-filter:blur(14px); }
    .settings-dirty { padding:4px 7px; border:1px solid var(--line2); border-radius:999px; color:var(--muted); background:#0b110d; font:650 9px/1 ui-monospace,Consolas,monospace; letter-spacing:.06em; text-transform:uppercase; }
    .settings-dirty.changed { color:var(--amber); border-color:#705b32; background:#20190d; }
    .settings-dirty.saved { color:var(--mint); border-color:#38644d; background:#0e1e15; }
    .settings-overview { display:grid; grid-template-columns:minmax(260px,.7fr) minmax(320px,1.3fr); border-bottom:1px solid var(--line); background:#090e0b; }
    .readiness-card { display:flex; align-items:center; gap:13px; padding:14px 16px; border-right:1px solid var(--line); }
    .readiness-ring { --progress:0deg; width:54px; height:54px; flex:0 0 auto; display:grid; place-items:center; border-radius:50%; color:var(--lime); background:radial-gradient(circle at center,#0c130e 55%,transparent 57%),conic-gradient(var(--lime) var(--progress),#233027 0); font:750 11px/1 ui-monospace,Consolas,monospace; }
    .readiness-card strong { display:block; margin-bottom:4px; font-size:12px; }
    .settings-overview .settings-note { display:flex; align-items:center; border:0; padding:14px 16px; }
    .settings-layout { display:grid; grid-template-columns:220px minmax(0,1fr); min-height:620px; }
    .settings-nav { padding:10px; border-right:1px solid var(--line); background:#080d0a; }
    .settings-nav button { width:100%; min-height:44px; display:grid; grid-template-columns:26px 1fr auto; align-items:center; gap:7px; margin-bottom:4px; padding:7px 9px; border-color:transparent; color:#9eaaa0; background:transparent; text-align:left; }
    .settings-nav button:hover:not(:disabled),.settings-nav button.active { transform:none; color:var(--text); border-color:var(--line2); background:#131c16; }
    .settings-nav button.active { color:var(--lime); box-shadow:inset 3px 0 var(--lime); }
    .nav-icon { color:var(--muted); font:750 9px/1 ui-monospace,Consolas,monospace; }
    .nav-label { min-width:0; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; font-size:11px; font-weight:670; }
    .nav-count { color:#738078; font:650 9px/1 ui-monospace,Consolas,monospace; }
    .settings-page { min-width:0; background:#0b100d; }
    .settings-page-head { min-height:79px; display:flex; align-items:flex-start; justify-content:space-between; gap:16px; padding:17px 18px; border-bottom:1px solid var(--line); }
    .settings-page-head h3 { margin:0 0 5px; font-size:17px; }
    .settings-page-head p { max-width:760px; margin:0; color:var(--muted); font-size:12px; }
    .settings-validation { display:none; margin:12px 14px 0; padding:10px 12px; border:1px solid #705c31; border-radius:8px; color:#ffd898; background:#211a0d; font-size:11px; }
    .settings-validation.show { display:block; }
    .settings-validation.error { border-color:#733e38; color:#ffc0b9; background:#261311; }
    .settings-editor { display:block; max-height:none; overflow:visible; padding:14px; }
    .source-guide { margin:14px 14px 0; border:1px solid #4c6544; border-radius:10px; overflow:hidden; background:#0d160f; }
    .source-guide-head { padding:14px 16px; border-bottom:1px solid var(--line); background:linear-gradient(90deg,#18271a,transparent); }
    .source-guide-head h4 { margin:0 0 5px; color:var(--lime); font-size:14px; }
    .source-guide-head p { margin:0; color:#a6b2a8; font-size:11px; }
    .server-builder-head { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:11px 13px; border-bottom:1px solid var(--line); background:#0a100c; }
    .server-builder-head p { margin:0; color:var(--muted); font-size:10px; }
    .server-builder { display:grid; gap:9px; padding:10px; }
    .server-group { border:1px solid var(--line2); border-radius:9px; overflow:hidden; background:#090f0b; }
    .server-group-head { display:flex; align-items:center; justify-content:space-between; gap:10px; padding:9px 11px; border-bottom:1px solid var(--line); background:#111a13; }
    .server-group-head strong { color:var(--lime); font-size:11px; }
    .server-fields { display:grid; grid-template-columns:minmax(180px,.75fr) minmax(240px,1.25fr); gap:9px; padding:10px; }
    .server-field label,.channel-field label { display:block; margin-bottom:5px; color:#9aa79d; font:650 9px/1.2 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .server-storage-hint { display:block; margin-top:6px; color:var(--lime); font:650 8px/1.45 ui-monospace,Consolas,monospace; }
    .server-mode { display:flex; align-items:center; justify-content:space-between; gap:12px; margin:0 10px 10px; padding:9px 10px; border:1px solid var(--line); border-radius:8px; background:#0d140f; }
    .server-mode div { color:#aab5ac; font-size:10px; }
    .server-mode small { display:block; margin-top:3px; color:var(--muted); }
    .server-mode input { width:17px; height:17px; accent-color:var(--lime); }
    .server-channels { margin:0 10px 10px; border:1px solid var(--line); border-radius:8px; overflow:hidden; }
    .server-channels-head { display:flex; align-items:center; justify-content:space-between; gap:10px; padding:8px 10px; border-bottom:1px solid var(--line); background:#0e1510; }
    .server-channels-head span { color:#aab5ac; font-size:10px; font-weight:680; }
    .server-channels-head button { min-height:31px; padding:5px 9px; }
    .channel-row { display:grid; grid-template-columns:minmax(170px,.75fr) minmax(220px,1.25fr) auto; align-items:end; gap:8px; padding:9px; border-bottom:1px solid var(--line); }
    .channel-row:last-child { border-bottom:0; }
    .channel-row button { min-height:40px; }
    .channel-storage-actions { display:flex; gap:6px; margin-top:5px; }
    .channel-storage-actions button { min-height:26px; padding:3px 7px; font-size:9px; }
    .channel-storage-state { align-self:center; color:var(--muted); font:650 8px/1.35 ui-monospace,Consolas,monospace; }
    .channel-storage-state.auto { color:var(--lime); }
    .standalone-channels { margin:0 10px 10px; border:1px dashed #685637; border-radius:8px; overflow:hidden; }
    .server-builder-empty { padding:24px 12px; color:var(--muted); text-align:center; font-size:11px; }
    .source-priority { margin:0 10px 10px; padding:9px 11px; border-left:3px solid var(--amber); color:#b8c1ba; background:#15150d; font-size:10px; }
    .source-warning { margin:0 10px 10px; padding:9px 11px; border:1px solid #6f3d35; border-radius:7px; color:#ffbbb2; background:#241310; font-size:10px; }
    .category-grid,.setting-object-body,.collection-fields { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:9px; }
    .setting-field { position:relative; min-height:116px; padding:13px; }
    .setting-field.wide,.setting-object,.setting-collection { grid-column:1/-1; }
    .field-heading { display:flex; align-items:flex-start; justify-content:space-between; gap:10px; margin-bottom:4px; }
    .field-heading label,.field-heading .field-label { color:var(--text); font-size:12px; font-weight:680; letter-spacing:0; }
    .field-path { color:#647168; font:600 8px/1 ui-monospace,Consolas,monospace; }
    .scope-badge { display:inline-block; margin-right:6px; padding:3px 5px; border:1px solid #52684b; border-radius:999px; color:var(--lime); font-size:7px; letter-spacing:.06em; text-transform:uppercase; }
    .field-help { min-height:34px; margin:0 0 10px; color:#7f8d82; font-size:10px; line-height:1.45; }
    .field-control { display:flex; align-items:center; gap:8px; }
    .field-control input,.field-control select { flex:1; min-width:0; }
    select { width:100%; height:42px; border:1px solid var(--line2); border-radius:8px; padding:0 10px; color:var(--text); background:#070b08; outline:none; }
    .field-unit { color:var(--muted); font:650 9px/1 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .toggle-control { display:inline-flex; align-items:center; gap:9px; cursor:pointer; user-select:none; }
    .toggle-control input { position:absolute; opacity:0; pointer-events:none; }
    .toggle-track { position:relative; width:40px; height:22px; border:1px solid #465349; border-radius:999px; background:#171f19; transition:.2s ease; }
    .toggle-track:after { content:""; position:absolute; width:16px; height:16px; left:2px; top:2px; border-radius:50%; background:#77827a; transition:.2s ease; }
    .toggle-control input:checked + .toggle-track { border-color:#739555; background:#27391f; }
    .toggle-control input:checked + .toggle-track:after { left:20px; background:var(--lime); }
    .toggle-state { color:var(--muted); font:650 9px/1 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .optional-control { display:grid; grid-template-columns:130px minmax(0,1fr); gap:8px; }
    .optional-control.inherited > :last-child { opacity:.45; pointer-events:none; }
    .setting-object,.setting-collection { border:1px solid var(--line2); border-radius:9px; background:#080d0a; }
    .setting-object summary { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:13px 14px; color:var(--text); }
    .setting-object summary small { color:var(--muted); font-weight:500; }
    .setting-object-body { padding:0 10px 10px; }
    .collection-head { display:flex; align-items:center; justify-content:space-between; gap:12px; padding:12px 13px; border-bottom:1px solid var(--line); }
    .collection-head h4 { margin:0; font-size:12px; }
    .collection-head p { margin:3px 0 0; color:var(--muted); font-size:10px; }
    .collection-head button { min-height:32px; padding:5px 9px; }
    .collection-body { display:grid; gap:8px; padding:9px; }
    .collection-empty { padding:22px 10px; color:var(--muted); text-align:center; font-size:11px; }
    .collection-item { border:1px solid var(--line); border-radius:8px; background:#0d140f; }
    .collection-item-head { display:flex; align-items:center; justify-content:space-between; min-height:38px; padding:5px 8px 5px 11px; border-bottom:1px solid var(--line); color:var(--muted); font:650 9px/1 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .collection-item-head button { min-height:27px; padding:3px 8px; }
    .collection-fields { padding:9px; }
    .primitive-row { display:grid; grid-template-columns:1fr auto; gap:7px; padding:8px; }
    .json-warning { margin-top:7px; color:var(--amber); font-size:9px; }
    .settings-empty { padding:54px 20px; }
    @media (max-width:1000px) { .settings-layout { grid-template-columns:185px minmax(0,1fr); } .settings-actions { justify-content:flex-start; } }
    @media (max-width:760px) { .settings-overview { grid-template-columns:1fr; } .readiness-card { border-right:0; border-bottom:1px solid var(--line); } .settings-layout { grid-template-columns:1fr; } .settings-nav { display:flex; gap:5px; overflow:auto; border-right:0; border-bottom:1px solid var(--line); } .settings-nav button { width:auto; min-width:130px; margin:0; } .category-grid,.setting-object-body,.collection-fields,.server-fields,.channel-row { grid-template-columns:1fr; } .channel-row { align-items:stretch; } .settings-hub-head { position:static; } }
    @media (max-width:520px) { .settings-page-head { padding:14px; } .optional-control { grid-template-columns:1fr; } .settings-actions button { flex:1 1 30%; } }

    /* Device-local color themes. */
    :root { --ambient:#183524; --page-a:#050806; --page-b:#080d09; --page-c:#050706; --panel-a:#0e1510; --panel-b:#090e0b; }
    :root[data-theme="ocean"] { --lime:#67abff; --mint:#50d4e8; --amber:#83c7ff; --coral:#ff7f75; --line:#223449; --line2:#344e6c; --ambient:#17375b; --page-a:#05080d; --page-b:#07101b; --page-c:#04070b; --panel-a:#0b1521; --panel-b:#070d15; }
    :root[data-theme="violet"] { --lime:#b694ff; --mint:#e079dd; --amber:#d8aeff; --coral:#ff7799; --line:#392b4d; --line2:#563e72; --ambient:#3b205a; --page-a:#09060d; --page-b:#120b19; --page-c:#070509; --panel-a:#181020; --panel-b:#0e0913; }
    :root[data-theme="amber"] { --lime:#ffc65c; --mint:#ff9f5a; --amber:#ffe18c; --coral:#ff755e; --line:#473821; --line2:#69502b; --ambient:#563813; --page-a:#0c0904; --page-b:#151005; --page-c:#090703; --panel-a:#1b1408; --panel-b:#100c05; }
    :root[data-theme="crimson"] { --lime:#ff7770; --mint:#ff9b7f; --amber:#ffc07c; --coral:#ff5c73; --line:#4a292b; --line2:#6c3b3e; --ambient:#5b1d28; --page-a:#0c0506; --page-b:#16090b; --page-c:#090405; --panel-a:#1c0e11; --panel-b:#100709; }
    :root[data-theme="mono"] { --lime:#f2f2ed; --mint:#b7c0ba; --amber:#d7d7d0; --coral:#eeeeea; --line:#343836; --line2:#505552; --ambient:#343837; --page-a:#080909; --page-b:#0e1010; --page-c:#060707; --panel-a:#151817; --panel-b:#0b0d0c; }
    :root { --logo-primary:#b7f36b; --logo-secondary:#49d7a4; --logo-deep:#27432a; --logo-arrow-a:#efffc7; --logo-arrow-b:#49d7a4; --logo-detail:#f4f2e9; --logo-detail-muted:#c7e49f; --logo-accent:#b7f36b; --logo-background:#101b12; }
    :root[data-theme="ocean"] { --logo-primary:#67abff; --logo-secondary:#50d4e8; --logo-deep:#1d3d73; --logo-arrow-a:#eef7ff; --logo-arrow-b:#50d4e8; --logo-detail:#eef6ff; --logo-detail-muted:#a9cde8; --logo-accent:#67abff; --logo-background:#0b1422; }
    :root[data-theme="violet"] { --logo-primary:#b694ff; --logo-secondary:#e079dd; --logo-deep:#5b2b76; --logo-arrow-a:#f5eaff; --logo-arrow-b:#e079dd; --logo-detail:#f8f1ff; --logo-detail-muted:#d8b9ef; --logo-accent:#b694ff; --logo-background:#1a1024; }
    :root[data-theme="amber"] { --logo-primary:#ffc65c; --logo-secondary:#ff9f5a; --logo-deep:#74401d; --logo-arrow-a:#fff3bd; --logo-arrow-b:#ff9f5a; --logo-detail:#fff8e8; --logo-detail-muted:#f0c27f; --logo-accent:#ffc65c; --logo-background:#211508; }
    :root[data-theme="crimson"] { --logo-primary:#ff7770; --logo-secondary:#ff9b7f; --logo-deep:#75243a; --logo-arrow-a:#ffe3dc; --logo-arrow-b:#ff9b7f; --logo-detail:#fff1ef; --logo-detail-muted:#f0a9a2; --logo-accent:#ff7770; --logo-background:#251015; }
    :root[data-theme="mono"] { --logo-primary:#f2f2ed; --logo-secondary:#b7c0ba; --logo-deep:#59615c; --logo-arrow-a:#ffffff; --logo-arrow-b:#b7c0ba; --logo-detail:#ffffff; --logo-detail-muted:#c8cec9; --logo-accent:#f2f2ed; --logo-background:#171a18; }
    body { background:radial-gradient(circle at 74% -20%,var(--ambient) 0,transparent 38%),linear-gradient(145deg,var(--page-a) 0,var(--page-b) 52%,var(--page-c) 100%); }
    .panel,.telemetry,.side-card { background:linear-gradient(145deg,var(--panel-a),var(--panel-b)); }
    .control-picker { padding:3px 5px 3px 10px; }
    .control-picker select { width:auto; height:27px; min-width:88px; margin-left:2px; padding:0 24px 0 8px; border:0; color:var(--lime); background:#0000; font:700 10px/1 ui-monospace,Consolas,monospace; text-transform:uppercase; }
    .locale-picker select { min-width:48px; }
    .control-picker select option { color:var(--text); background:var(--panel-a); }
    .console-panel .panel-head { align-items:center; }
    .console-tools { display:flex; align-items:center; gap:8px; flex-wrap:wrap; }
    .console-tools button { min-height:32px; padding:5px 10px; }
    .console-output { height:330px; margin:0; overflow:auto; padding:10px 0; color:#c8d6ca; background:#050806; white-space:pre-wrap; overflow-wrap:anywhere; tab-size:4; font:11px/1.58 ui-monospace,SFMono-Regular,Consolas,"Liberation Mono",monospace; scrollbar-width:thin; scrollbar-color:var(--line2) transparent; }
    .console-output:empty { padding:14px 16px; }
    .console-output:empty:before { content:attr(data-empty); color:var(--muted); }
    .console-line { display:block; min-height:18px; padding:1px 16px 1px 13px; border-left:3px solid transparent; color:#c8d6ca; }
    .console-line:hover { background:#ffffff08; }
    .console-line.info { border-left-color:#5582a4; color:#a9cde8; }
    .console-line.success { border-left-color:var(--mint); color:#8fe5bd; background:#49d7a407; }
    .console-line.warning { border-left-color:var(--amber); color:#ffd08b; background:#ffbe6408; }
    .console-line.error { border-left-color:var(--coral); color:#ff9b93; background:#ff72670b; }
    .console-line.debug { border-left-color:#ad8cff; color:#c8b8ee; }
    .console-line.discord { border-left-color:var(--blue); color:#a9c4ff; }
    .console-line.json { color:#819087; }
    .console-line .log-time { color:#65766b; }
    .console-line .log-source { color:#d9e6dc; font-weight:700; }
    .console-line .log-tag { color:var(--lime); font-weight:750; }
    .console-status { color:var(--muted); font:10px/1 ui-monospace,Consolas,monospace; }
    @media (max-width:760px) { .control-picker > span { display:none; } .control-picker select { min-width:68px; padding-left:3px; } .locale-picker select { min-width:42px; } }
  </style>
</head>
<body>
  <main class="app-shell">
    <header class="topbar">
      <div class="brand-lockup">
        <div class="brand-mark" aria-hidden="true">{{GUI_LOGO}}</div>
        <div><div class="brand-name">Archive <em>Relay</em></div><div class="brand-caption">Discord media operations</div></div>
      </div>
      <div class="topbar-actions">
        <label class="pill control-picker locale-picker" for="languageSelect"><span data-i18n="language">Bahasa</span><select id="languageSelect" aria-label="Bahasa dashboard"><option value="id">ID</option><option value="en">EN</option></select></label>
        <label class="pill control-picker theme-picker" for="themeSelect"><span data-i18n="theme">Tema</span><select id="themeSelect" aria-label="Tema dashboard"><option value="forest" data-i18n="themeForest">Hijau</option><option value="ocean" data-i18n="themeOcean">Biru</option><option value="violet" data-i18n="themeViolet">Ungu</option><option value="amber">Amber</option><option value="crimson" data-i18n="themeCrimson">Merah</option><option value="mono" data-i18n="themeMono">Monokrom</option></select></label>
        <div class="pill">Build <strong id="version">v--</strong></div>
        <div class="pill"><span id="readyDot" class="dot"></span><strong id="readyText">Menghubungkan</strong></div>
        <div class="pill" id="uptimeValue">Uptime --</div>
      </div>
    </header>
    <div class="setup" id="setupBanner" role="alert"></div>

    <div class="workspace">
      <aside class="sidebar" aria-label="Kontrol arsip">
        <section class="side-card">
          <div class="panel-kicker"><span data-i18n="activeScope">Cakupan aktif</span></div>
          <div class="scope-list" id="configGrid"><div>Status</div><div data-i18n="loading">Memuat...</div></div>
        </section>

        <section class="side-card">
          <div class="panel-kicker"><span data-i18n="sessionControl">Kontrol sesi</span></div>
          <h2 data-i18n="systemHealth">Kesehatan sistem</h2>
          <div class="runtime-control">
            <div class="runtime-state"><span data-i18n="runtimeStatus">Status runtime</span><strong id="runtimeState">Menyiapkan</strong></div>
            <button id="runtimeStartButton" class="runtime-start" type="button" disabled data-i18n="startRuntime">Start aplikasi</button>
          </div>
          <p class="health-copy" id="healthNotes" data-i18n="waitingTelemetry">Menunggu telemetri runtime...</p>
          <div class="side-actions">
            <button id="liveToggle" class="secondary active" type="button" aria-pressed="true" data-i18n="liveOn">Live: aktif</button>
            <button id="copySnapshot" class="secondary" type="button" data-i18n="copyStatus">Salin status</button>
          </div>
        </section>
      </aside>

      <section class="content" aria-label="Telemetri dan aktivitas">
        <section class="telemetry" aria-labelledby="telemetryTitle">
          <div class="hero-stat">
            <div class="microcopy" id="telemetryTitle" data-i18n="totalMedia">Total media tersimpan</div>
            <div class="hero-number"><span id="totalDownloads">--</span><small>file</small></div>
            <div class="telemetry-note" id="lastDownload" data-i18n="noRecentDownload">Belum ada unduhan terbaru</div>
            <div class="signal" id="activityPulse" aria-hidden="true"></div>
          </div>
          <div class="telemetry-stat"><div class="microcopy" data-i18n="transfer">Transfer</div><div class="telemetry-value" id="queueValue">--</div><div class="telemetry-note" id="workerValue" data-i18n="preparingWorkers">Menyiapkan worker</div></div>
          <div class="telemetry-stat"><div class="microcopy" data-i18n="history">Riwayat</div><div class="telemetry-value" id="historyValue">--</div><div class="telemetry-note" id="historyHint" data-i18n="noJobs">Belum ada job</div></div>
          <div class="telemetry-stat"><div class="microcopy" data-i18n="connectedServers">Server terhubung</div><div class="telemetry-value" id="guildValue">--</div><div class="telemetry-note" data-i18n="monitoredByBot">Terpantau oleh bot</div></div>
        </section>

        <section class="panel">
          <div class="panel-head">
            <div class="panel-title"><h2 data-i18n="scanActivity">Aktivitas pemindaian</h2><span class="count-chip" id="jobCount">0</span></div>
            <div class="activity-tools"><label class="screenreader" for="jobsSearch" data-i18n="searchJobs">Cari job</label><input id="jobsSearch" type="search" placeholder="Cari channel atau status" data-i18n-placeholder="searchJobsPlaceholder"><button id="refreshButton" class="secondary" type="button" data-i18n="refresh">Segarkan</button></div>
          </div>
          <nav class="job-tabs" aria-label="Filter aktivitas">
            <button class="active" type="button" data-job-filter="all" aria-pressed="true" data-i18n="all">Semua</button>
            <button type="button" data-job-filter="active" aria-pressed="false" data-i18n="active">Aktif</button>
            <button type="button" data-job-filter="completed" aria-pressed="false" data-i18n="completed">Selesai</button>
            <button type="button" data-job-filter="failed" aria-pressed="false" data-i18n="failed">Gagal</button>
          </nav>
          <div class="table-wrap"><table><thead><tr><th>Channel</th><th>Status</th><th data-i18n="source">Sumber</th><th data-i18n="updated">Diperbarui</th><th data-i18n="action">Aksi</th></tr></thead><tbody id="jobsBody"></tbody></table><div id="emptyJobs" class="empty" data-i18n="noMatchingActivity">Belum ada aktivitas yang cocok.</div></div>
        </section>

        <section class="panel console-panel">
          <div class="panel-head">
            <div class="panel-title"><h2 data-i18n="runtimeConsole">Console aplikasi</h2><span class="count-chip" id="consoleCount">0</span></div>
            <div class="console-tools">
              <span class="console-status" id="consoleStatus" data-i18n="consoleLive">LOG LIVE</span>
              <button id="consoleFollow" class="secondary active" type="button" aria-pressed="true" data-i18n="autoScrollOn">Auto-scroll: aktif</button>
              <button id="consoleCopy" class="secondary" type="button" data-i18n="copyLogs">Salin log</button>
              <button id="consoleClear" class="secondary" type="button" data-i18n="clearView">Bersihkan tampilan</button>
            </div>
          </div>
          <pre class="console-output" id="consoleOutput" data-empty="Menunggu output aplikasi..." aria-live="polite"></pre>
        </section>

        <section class="panel settings-panel settings-hub">
          <div class="panel-head settings-hub-head">
            <div>
              <div class="panel-title"><h2 data-i18n="configCenter">Pusat konfigurasi</h2><span class="settings-dirty" id="settingsDirty" data-i18n="notLoaded">Belum dimuat</span></div>
              <div class="subtitle" id="settingsFile" data-i18n="loadingSettings">Memuat file pengaturan...</div>
            </div>
            <div class="settings-actions">
              <div class="settings-mode" role="group" aria-label="Mode pengaturan">
                <button id="settingsModeNormal" class="active" type="button" aria-pressed="true" data-i18n="normalMode">Normal</button>
                <button id="settingsModeAdvanced" type="button" aria-pressed="false" data-i18n="advancedMode">Advanced</button>
              </div>
              <label class="screenreader" for="settingsSearch" data-i18n="searchOptions">Cari opsi</label><input id="settingsSearch" type="search" placeholder="Cari fitur, opsi, atau penjelasan" data-i18n-placeholder="searchSettingsPlaceholder">
              <button id="settingsImport" class="secondary" type="button" data-i18n="import">Impor</button><input id="settingsImportFile" class="screenreader" type="file" accept=".json,application/json">
              <button id="settingsExport" class="secondary" type="button" data-i18n="export">Ekspor</button>
              <button id="settingsReload" class="secondary" type="button" data-i18n="reload">Muat ulang</button>
              <button id="settingsDiscard" class="secondary" type="button" disabled data-i18n="discard">Batalkan</button>
              <button id="settingsSave" type="button" disabled data-i18n="saveChanges">Simpan perubahan</button>
            </div>
          </div>
          <div class="settings-mode-note" id="settingsModeNote"><strong id="settingsModeName" data-i18n="normalMode">Normal</strong> · <span id="settingsModeHelp" data-i18n="normalModeHelp">Hanya opsi penting untuk penggunaan sehari-hari. Beralih ke Advanced untuk melihat seluruh konfigurasi.</span></div>
          <div class="settings-overview">
            <div class="readiness-card"><div class="readiness-ring" id="settingsProgress">0%</div><div><strong data-i18n="configReadiness">Kesiapan konfigurasi</strong><div class="subtitle" id="settingsChecklist" data-i18n="checkingComponents">Memeriksa komponen utama...</div></div></div>
            <div class="settings-note" data-i18n="settingsSecurityNote">Editor ini mencakup seluruh fitur aplikasi. Nilai sensitif tidak pernah ditampilkan kembali; biarkan kosong untuk mempertahankan nilai yang sudah tersimpan. Penyimpanan selalu membuat backup.</div>
          </div>
          <div class="settings-layout">
            <nav class="settings-nav" id="settingsNav" aria-label="Kategori pengaturan"></nav>
            <div class="settings-page">
              <div class="settings-page-head"><div><h3 id="settingsCategoryTitle" data-i18n="settings">Pengaturan</h3><p id="settingsCategoryDesc" data-i18n="selectCategory">Pilih kategori untuk mulai mengatur aplikasi.</p></div><span class="count-chip" id="settingsCount">0</span></div>
              <div class="settings-validation" id="settingsValidation" role="status"></div>
              <div class="settings-editor" id="settingsEditor"><div class="settings-empty" data-i18n="loadingOptions">Memuat semua opsi...</div></div>
            </div>
          </div>
        </section>
      </section>
    </div>
    <div class="brand-caption" style="margin:18px 4px 0">Local-only control surface / Discord Archive Console</div>
  </main><div id="toast" class="toast" role="status" aria-live="polite"></div>
  <script>
    const token=document.querySelector('meta[name="ddg-token"]').content;
    const byId=(id)=>document.getElementById(id);
    const availableThemes=new Set(['forest','ocean','violet','amber','crimson','mono']);
    let currentLanguage='en';
    const uiTranslations={
      language:['Bahasa','Language'],theme:['Tema','Theme'],themeForest:['Hijau','Green'],themeOcean:['Biru','Blue'],themeViolet:['Ungu','Violet'],themeCrimson:['Merah','Red'],themeMono:['Monokrom','Monochrome'],loading:['Memuat...','Loading...'],
      activeScope:['Cakupan aktif','Active scope'],sessionControl:['Kontrol sesi','Session controls'],systemHealth:['Kesehatan sistem','System health'],runtimeStatus:['Status runtime','Runtime status'],startRuntime:['Start aplikasi','Start application'],runtimeWaiting:['Menunggu Start','Waiting for Start'],runtimeStarting:['Memulai...','Starting...'],runtimeRunning:['Berjalan','Running'],runtimeStopping:['Berhenti...','Stopping...'],runtimeInitializing:['Menyiapkan','Initializing'],waitingStartHelp:['Periksa Settings, lalu tekan Start saat siap. Belum ada koneksi Discord atau worker yang berjalan.','Review Settings, then press Start when ready. Discord and workers are not running yet.'],startingHelp:['Runtime sedang membuka database dan menghubungkan Discord.','The runtime is opening the database and connecting to Discord.'],waitingTelemetry:['Menunggu telemetri runtime...','Waiting for runtime telemetry...'],liveOn:['Live: aktif','Live: on'],livePaused:['Live: jeda','Live: paused'],copyStatus:['Salin status','Copy status'],
      totalMedia:['Total media tersimpan','Total media archived'],noRecentDownload:['Belum ada unduhan terbaru','No recent download'],transfer:['Transfer','Transfers'],preparingWorkers:['Menyiapkan worker','Preparing workers'],history:['Riwayat','History'],noJobs:['Belum ada job','No jobs yet'],connectedServers:['Server terhubung','Connected servers'],monitoredByBot:['Terpantau oleh bot','Monitored by the bot'],scanActivity:['Aktivitas pemindaian','Scan activity'],searchJobs:['Cari job','Search jobs'],searchJobsPlaceholder:['Cari channel atau status','Search channel or status'],refresh:['Segarkan','Refresh'],all:['Semua','All'],active:['Aktif','Active'],completed:['Selesai','Completed'],failed:['Gagal','Failed'],source:['Sumber','Source'],updated:['Diperbarui','Updated'],action:['Aksi','Action'],noMatchingActivity:['Belum ada aktivitas yang cocok.','No matching activity.'],
      configCenter:['Pusat konfigurasi','Configuration center'],notLoaded:['Belum dimuat','Not loaded'],loadingSettings:['Memuat file pengaturan...','Loading settings file...'],searchOptions:['Cari opsi','Search options'],searchSettingsPlaceholder:['Cari fitur, opsi, atau penjelasan','Search features, options, or help'],import:['Impor','Import'],export:['Ekspor','Export'],reload:['Muat ulang','Reload'],discard:['Batalkan','Discard'],saveChanges:['Simpan perubahan','Save changes'],configReadiness:['Kesiapan konfigurasi','Configuration readiness'],checkingComponents:['Memeriksa komponen utama...','Checking core components...'],settingsSecurityNote:['Nilai sensitif tidak pernah ditampilkan kembali; biarkan kosong untuk mempertahankan nilai yang sudah tersimpan. Penyimpanan selalu membuat backup.','Sensitive values are never shown again; leave them blank to preserve stored values. Every save creates a backup.'],settings:['Pengaturan','Settings'],selectCategory:['Pilih kategori untuk mulai mengatur aplikasi.','Select a category to configure the application.'],loadingOptions:['Memuat semua opsi...','Loading all options...'],normalMode:['Normal','Normal'],advancedMode:['Lanjutan','Advanced'],normalModeHelp:['Hanya opsi penting untuk penggunaan sehari-hari. Beralih ke Lanjutan untuk melihat seluruh konfigurasi.','Only essential everyday options are shown. Switch to Advanced to see the complete configuration.'],advancedModeHelp:['Seluruh opsi dan override teknis ditampilkan. Ubah hanya pengaturan yang kamu pahami.','Every option and technical override is visible. Change only settings you understand.'],
      runtimeConsole:['Console aplikasi','Application console'],consoleLive:['LOG LIVE','LIVE LOG'],autoScrollOn:['Auto-scroll: aktif','Auto-scroll: on'],autoScrollOff:['Auto-scroll: mati','Auto-scroll: off'],copyLogs:['Salin log','Copy logs'],clearView:['Bersihkan tampilan','Clear view'],waitingLogs:['Menunggu output aplikasi...','Waiting for application output...'],
      cancel:['Batalkan','Cancel'],never:['tidak pernah','never'],secondsAgo:['{n} dtk lalu','{n}s ago'],minutesAgo:['{n} mnt lalu','{n}m ago'],hoursAgo:['{n} jam lalu','{n}h ago'],daysAgo:['{n} hari lalu','{n}d ago'],online:['online','online'],discordNotReady:['Discord belum siap','Discord not ready'],lastDownloaded:['Unduhan terakhir {time}','Last download {time}'],activeTransfers:['{n} aktif','{n} active'],workerSummary:['{queued} antre · {workers} worker','{queued} queued · {workers} workers'],historyRunning:['{n} jalan','{n} running'],historySummary:['{waiting} tunggu · {failed} gagal','{waiting} waiting · {failed} failed'],
      settingsFileRestart:['{file} · perubahan penuh diterapkan setelah restart','{file} · restart to apply all changes'],settingsFileStart:['{file} · opsi runtime dimuat saat Start; opsi peluncuran GUI perlu restart','{file} · runtime options load on Start; GUI launch options need a restart'],noSettings:['Pengaturan tidak tersedia.','Settings are unavailable.'],searchResults:['Hasil pencarian','Search results'],searchResultsHelp:['Semua opsi yang cocok dari seluruh kategori.','Matching options from every category.'],noMatchingSettings:['Tidak ada pengaturan yang cocok.','No matching settings.'],enabled:['Aktif','Enabled'],disabled:['Mati','Disabled'],inherit:['Ikuti induk','Inherit'],customValue:['Gunakan nilai khusus','Use custom value'],activateConfig:['Aktifkan konfigurasi','Enable configuration'],add:['+ Tambah','+ Add'],emptyList:['Daftar masih kosong. Klik “Tambah” untuk membuat entri pertama.','This list is empty. Click “Add” to create the first entry.'],remove:['Hapus','Remove'],removeEntry:['Hapus entri #{n}','Remove entry #{n}'],configuredSecret:['Sudah tersimpan — kosongkan untuk mempertahankan','Stored — leave blank to preserve'],dynamicJSON:['Objek dengan key dinamis menggunakan format JSON.','Objects with dynamic keys use JSON format.'],overrideHelp:[' Opsi ini merupakan override khusus sumber, bukan duplikat pengaturan global.',' This is a source-specific override, not a duplicate of the global setting.'],inheritHelp:[' Pilih “Ikuti induk” untuk memakai nilai dari cakupan yang lebih umum.',' Choose “Inherit” to use the value from the broader scope.']
    };
    function tx(key,values={}){const pair=uiTranslations[key],template=pair?(pair[currentLanguage==='en'?1:0]):key;return String(template).replace(/\{(\w+)\}/g,(_,name)=>values[name]??'');}
    const bi=(indonesian,english)=>currentLanguage==='en'?english:indonesian;
    function translateStatic(){document.querySelectorAll('[data-i18n]').forEach((element)=>element.textContent=tx(element.dataset.i18n));document.querySelectorAll('[data-i18n-placeholder]').forEach((element)=>element.placeholder=tx(element.dataset.i18nPlaceholder));}
    function applyLanguage(language,persist=true){currentLanguage=language==='en'?'en':'id';document.documentElement.lang=currentLanguage;byId('languageSelect').value=currentLanguage;translateStatic();if(persist){try{localStorage.setItem('archive-relay-language',currentLanguage);}catch(_){}}}
    function applyTheme(theme,persist=true){const selected=availableThemes.has(theme)?theme:'forest';document.documentElement.dataset.theme=selected;byId('themeSelect').value=selected;if(persist){try{localStorage.setItem('archive-relay-theme',selected);}catch(_){}}}
    try{applyTheme(localStorage.getItem('archive-relay-theme')||'forest',false);}catch(_){applyTheme('forest',false);}
    try{applyLanguage(localStorage.getItem('archive-relay-language')||'en',false);}catch(_){applyLanguage('en',false);}
    const escapeHtml=(value)=>String(value??'').replace(/[&<>"']/g,(c)=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    const relative=(iso)=>{if(!iso)return tx('never');const seconds=Math.max(0,Math.floor((Date.now()-Date.parse(iso))/1000));if(seconds<60)return tx('secondsAgo',{n:seconds});if(seconds<3600)return tx('minutesAgo',{n:Math.floor(seconds/60)});if(seconds<86400)return tx('hoursAgo',{n:Math.floor(seconds/3600)});return tx('daysAgo',{n:Math.floor(seconds/86400)});};
    const duration=(seconds)=>{seconds=Math.max(0,seconds||0);const h=Math.floor(seconds/3600),m=Math.floor((seconds%3600)/60),hour=currentLanguage==='en'?'h':'j',minute=currentLanguage==='en'?'m':'m';return h?h+hour+' '+m+minute:m+minute;};
    let settingsModel=null,settingsOriginal=null,settingsTemplates=null,settingsSecretFields={},settingsFilePath='',lastAddedSource=null,activeSettingsCategory='access',lastSettingsCategory='access',lastStatus=null,lastJobs=[],jobFilter='all',autoRefresh=true,settingsMode='normal';
    let consoleCursor=0,consoleLines=[],consoleAutoScroll=true,consoleLoading=false;
    try{settingsMode=localStorage.getItem('archive-relay-settings-mode')==='advanced'?'advanced':'normal';}catch(_){settingsMode='normal';}
    const cloneValue=(value)=>value===undefined?undefined:JSON.parse(JSON.stringify(value));
    const optionLabel=(name)=>String(name).replace(/^_/,'').replace(/([a-z0-9])([A-Z])/g,'$1 $2').replace(/^./,(c)=>c.toUpperCase());
    const optionSearch=(value)=>String(value).toLowerCase().replace(/[^a-z0-9]/g,'');
    const pathText=(path)=>path.join('.');
    const settingsCategories=[
      {id:'access',icon:'01',label:'Akses & akun',labelEn:'Access & accounts',description:'Token Discord, akun layanan media, administrator, dan channel kontrol.',descriptionEn:'Discord credentials, media service accounts, administrators, and control channels.',keys:['credentials','admins','adminChannels']},
      {id:'program',icon:'02',label:'Aplikasi & GUI',labelEn:'Application & GUI',description:'Perilaku proses, dashboard lokal, pemeriksaan koneksi, backup, dan pembaruan konfigurasi.',descriptionEn:'Process behavior, local dashboard, connection checks, backups, and configuration updates.',keys:['processLimit','githubUpdateChecking','exitOnBadConnection','watchSettings','guiEnabled','guiAddress','guiOpenBrowser','guiAutoStart','guiCloseToTray','guiStartMinimized','backupDatabaseOnStart','checkupRate','connectionCheck','connectionCheckRate','historyManagerRate','inflateDownloadCount','europeanNumbers']},
      {id:'discord',icon:'03',label:'Perilaku Discord',labelEn:'Discord behavior',description:'Pemindaian pesan, command bot, timeout Discord, dan respons error.',descriptionEn:'Message scanning, bot commands, Discord timeouts, and error responses.',keys:['scanEdits','ignoreBots','scanOwnMessages','allowCommands','allowGeneralCommands','commandPrefix','commandTagging','discordTimeout','sendErrorMessages']},
      {id:'downloads',icon:'04',label:'Mesin unduhan',labelEn:'Download engine',description:'Worker paralel, antrean, retry, redirect, timeout, dan batas ukuran transfer.',descriptionEn:'Parallel workers, queue capacity, retries, redirects, timeouts, and transfer size limits.',keys:['downloadTimeout','downloadRetryMax','downloadWorkers','historyDownloadWorkers','downloadQueueSize','downloadMaxFileSize','downloadMaxRedirects']},
      {id:'storage',icon:'05',label:'Penyimpanan & filter',labelEn:'Storage & filters',description:'Jenis file, struktur folder, nama file, deduplikasi gambar, filter izin/blokir, dan pencatatan media.',descriptionEn:'File types, folder structure, filenames, image deduplication, allow/block filters, and media logs.',keys:['save','subfolders','subfoldersFallback','filenameDateFormat','filenameFormat','filepathNormalizeText','filepathStripSymbols','saveImages','saveVideos','saveAudioFiles','saveTextFiles','saveOtherFiles','savePossibleDuplicates','delayHandling','delayHandlingHistory','filters','duplo','duploThreshold','logLinks','logMessages']},
      {id:'history',icon:'06',label:'Riwayat channel',labelEn:'Channel history',description:'Job history, rentang otomatis, ukuran batch, jeda request, status, dan penghentian otomatis.',descriptionEn:'History jobs, automatic ranges, batch size, request delay, status reporting, and automatic exit.',keys:['historyMaxJobs','autoHistory','autoHistoryExit','autoHistoryBefore','autoHistorySince','sendAutoHistoryStatus','sendHistoryStatus','outputHistoryStatus','outputHistoryErrors','historyRequestCount','historyRequestDelay']},
      {id:'sources',icon:'07',label:'Sumber arsip',labelEn:'Archive sources',description:'Aturan global dan target khusus berdasarkan user, server, kategori, atau channel.',descriptionEn:'Global rules and specific targets by user, server, category, or channel.',keys:['all','allBlacklistUsers','allBlacklistServers','allBlacklistCategories','allBlacklistChannels','users','servers','categories','channels']},
      {id:'media',icon:'08',label:'Emoji & stiker',labelEn:'Emoji & stickers',description:'Pengambilan emoji/stiker, server pilihan, format nama, dan folder tujuan khusus.',descriptionEn:'Emoji and sticker capture, selected servers, filename formats, and dedicated destinations.',keys:['ignoreEmojis','ignoreStickers','ignoreEmojisWEBP','emojisServers','emojisFilenameFormat','emojisDestination','stickersServers','stickersFilenameFormat','stickersDestination']},
      {id:'forwarding',icon:'09',label:'Penerusan file',labelEn:'File forwarding',description:'Kirim ulang media ke satu atau beberapa channel Discord, langsung atau dengan caption.',descriptionEn:'Forward media to one or more Discord channels, directly or with a caption.',keys:['sendFileToChannel','sendFileToChannels','sendFileDirectly','sendFileCaption']},
      {id:'presence',icon:'10',label:'Presence & tampilan',labelEn:'Presence & appearance',description:'Status bot, aktivitas, reaksi unduhan, indikator typing, dan warna embed.',descriptionEn:'Bot status, activity, download reactions, typing indicator, and embed color.',keys:['presenceEnabled','presenceStatus','presenceType','presenceLabel','presenceDetails','presenceState','presenceRefreshRate','reactWhenDownloaded','reactWhenDownloadedEmoji','reactWhenDownloadedHistory','overwriteDefaultReaction','historyTyping','embedColor']},
      {id:'logging',icon:'11',label:'Log & diagnosis',labelEn:'Logs & diagnostics',description:'Tingkat detail log, output pesan, debugging, format indentasi, dan file log.',descriptionEn:'Log detail, message output, debugging, indentation, and log file destination.',keys:['verbose','debug','debugExtra','settingsOutput','logOutput','logIndent','messageOutput','messageOutputHistory','discordLogLevel']},
      {id:'paths',icon:'12',label:'Path internal',labelEn:'Internal paths',description:'Lokasi khusus untuk cache, riwayat, database, backup, Duplo, dan integrasi media.',descriptionEn:'Custom locations for cache, history, database, backups, Duplo, and media integrations.',keys:['overwriteCachePath','overwriteHistoryPath','overwriteDuploPath','overwriteTwitterPath','overwriteInstagramPath','overwriteConstantsPath','overwriteDatabasePath','overwriteDatabaseBackupsPath']},
      {id:'advanced',icon:'13',label:'Lanjutan',labelEn:'Advanced',description:'Konstanta dan opsi kompatibilitas yang tidak termasuk kategori utama.',descriptionEn:'Constants and compatibility options outside the primary categories.',keys:['_constants']}
    ];
    const normalSettingKeys=new Set(['credentials','watchSettings','guiEnabled','guiOpenBrowser','guiAutoStart','guiCloseToTray','guiStartMinimized','scanEdits','ignoreBots','allowCommands','commandPrefix','discordTimeout','downloadTimeout','downloadRetryMax','downloadWorkers','historyDownloadWorkers','downloadQueueSize','downloadMaxFileSize','save','subfolders','filenameFormat','saveImages','saveVideos','saveAudioFiles','saveTextFiles','saveOtherFiles','historyMaxJobs','historyRequestCount','historyRequestDelay','servers','channels','ignoreEmojis','ignoreStickers','presenceEnabled','presenceStatus','presenceType','presenceLabel','logOutput']);
    const normalNestedKeys={credentials:new Set(['token'])};
    const settingLabels={token:'Token bot Discord',email:'Email Discord (login lama)',password:'Kata sandi Discord (login lama)',twitterEnabled:'Aktifkan autentikasi Twitter',twitterUsername:'Username Twitter',twitterPassword:'Kata sandi Twitter',twitter2FA:'Kode 2FA Twitter',twitterProxy:'Proxy Twitter',twitterAuthToken:'Auth token Twitter',twitterCT0:'Cookie CT0 Twitter',instagramEnabled:'Aktifkan autentikasi Instagram',instagramUsername:'Username Instagram',instagramPassword:'Kata sandi Instagram',instagramTOTP:'Secret TOTP Instagram',instagramProxy:'Proxy Instagram',instagramProxyInsecure:'Izinkan TLS proxy Instagram tidak aman',instagramProxyForceHTTP2:'Paksa HTTP/2 pada proxy Instagram',flickrApiKey:'API key Flickr',admins:'Administrator bot',adminChannels:'Channel administrasi',guiEnabled:'Aktifkan dashboard GUI',guiAddress:'Alamat internal GUI',guiOpenBrowser:'Buka jendela aplikasi saat mulai',processLimit:'Batas proses paralel',downloadWorkers:'Worker unduhan',downloadQueueSize:'Kapasitas antrean',downloadMaxFileSize:'Ukuran file maksimum',downloadRetryMax:'Maksimum percobaan ulang',downloadMaxRedirects:'Maksimum redirect',downloadTimeout:'Timeout unduhan',discordTimeout:'Timeout Discord',commandPrefix:'Awalan command',historyMaxJobs:'Job history bersamaan',historyRequestCount:'Pesan per request',historyRequestDelay:'Jeda antar-request',filenameFormat:'Format nama file',filenameDateFormat:'Format tanggal file',subfolders:'Struktur subfolder',subfoldersFallback:'Subfolder cadangan',filters:'Filter konten',duplo:'Deteksi kemiripan gambar',duploThreshold:'Ambang kemiripan Duplo',all:'Aturan global',users:'Aturan per user',servers:'Aturan per server',categories:'Aturan per kategori',channels:'Aturan per channel',user:'User ID',server:'Server ID',category:'Category ID',channel:'Channel ID',destination:'Folder tujuan',presenceStatus:'Status Discord bot',presenceType:'Tipe aktivitas',presenceLabel:'Label aktivitas',presenceDetails:'Detail aktivitas',presenceState:'State aktivitas',embedColor:'Warna embed',sendFileToChannel:'Channel tujuan utama',sendFileToChannels:'Channel tujuan tambahan',sendFileDirectly:'Kirim file secara langsung',sendFileCaption:'Caption file',logOutput:'File output log',discordLogLevel:'Level log Discord',_constants:'Konstanta template'};
    const settingLabelsEN={token:'Discord bot token',email:'Discord email (legacy login)',password:'Discord password (legacy login)',twitterEnabled:'Enable Twitter authentication',twitterPassword:'Twitter password',twitter2FA:'Twitter 2FA code',twitterProxy:'Twitter proxy',twitterAuthToken:'Twitter auth token',twitterCT0:'Twitter CT0 cookie',instagramEnabled:'Enable Instagram authentication',instagramPassword:'Instagram password',instagramTOTP:'Instagram TOTP secret',instagramProxy:'Instagram proxy',instagramProxyInsecure:'Allow insecure Instagram proxy TLS',instagramProxyForceHTTP2:'Force HTTP/2 for Instagram proxy',flickrApiKey:'Flickr API key',admins:'Bot administrators',adminChannels:'Administration channels',guiEnabled:'Enable GUI dashboard',guiAddress:'Internal GUI address',guiOpenBrowser:'Open application window on startup',processLimit:'Parallel process limit',downloadWorkers:'Download workers',downloadQueueSize:'Queue capacity',downloadMaxFileSize:'Maximum file size',downloadRetryMax:'Maximum retries',downloadMaxRedirects:'Maximum redirects',downloadTimeout:'Download timeout',discordTimeout:'Discord timeout',commandPrefix:'Command prefix',historyMaxJobs:'Concurrent history jobs',historyRequestCount:'Messages per request',historyRequestDelay:'Delay between requests',filenameFormat:'Filename format',filenameDateFormat:'Filename date format',subfolders:'Subfolder structure',subfoldersFallback:'Fallback subfolders',filters:'Content filters',duplo:'Image similarity detection',duploThreshold:'Duplo similarity threshold',all:'Global rule',users:'Rules by user',servers:'Rules by server',categories:'Rules by category',channels:'Rules by channel',user:'User ID',server:'Server ID',category:'Category ID',channel:'Channel ID',destination:'Destination folder',presenceStatus:'Discord bot status',presenceType:'Activity type',presenceLabel:'Activity label',presenceDetails:'Activity details',presenceState:'Activity state',embedColor:'Embed color',sendFileToChannel:'Primary destination channel',sendFileToChannels:'Additional destination channels',sendFileDirectly:'Send files directly',sendFileCaption:'File caption',logOutput:'Log output file',discordLogLevel:'Discord log level',_constants:'Template constants'};
    Object.assign(settingLabels,{guiAutoStart:'Jalankan otomatis tanpa tombol Start',guiCloseToTray:'Tawarkan hide to tray saat ditutup',guiStartMinimized:'Mulai tersembunyi di system tray',historyDownloadWorkers:'Worker unduhan history'});
    Object.assign(settingLabelsEN,{guiAutoStart:'Run automatically without Start button',guiCloseToTray:'Offer hide to tray when closing',guiStartMinimized:'Start hidden in the system tray',historyDownloadWorkers:'History download workers'});
    const settingDescriptions={
      token:'Token bot dari Discord Developer Portal. Nilai tersimpan disembunyikan dan tidak akan terhapus bila kolom dibiarkan kosong.',
      email:'Email Discord hanya untuk metode login email lama. Untuk bot normal gunakan token bot. Nilai tersimpan disembunyikan.',password:'Kata sandi Discord yang berpasangan dengan Email Discord pada metode login lama; ini bukan kata sandi Instagram. Kosongkan untuk mempertahankan nilai tersimpan.',
      twitterUsername:'Nama akun Twitter/X yang digunakan resolver media.',twitterPassword:'Kata sandi khusus akun Twitter/X; terpisah dari kredensial Discord dan Instagram.',twitter2FA:'Kode atau secret 2FA untuk autentikasi akun Twitter/X.',twitterAuthToken:'Cookie auth_token Twitter/X untuk sesi yang sudah terautentikasi.',twitterCT0:'Cookie CSRF ct0 Twitter/X yang berpasangan dengan auth_token.',
      instagramUsername:'Nama akun Instagram yang digunakan resolver media; bukan Discord ID.',instagramPassword:'Kata sandi khusus akun Instagram; terpisah dari kata sandi Discord lama.',instagramTOTP:'Secret TOTP khusus Instagram untuk menghasilkan kode autentikasi dua faktor.',instagramProxy:'Proxy yang hanya digunakan untuk koneksi Instagram.',instagramProxyInsecure:'Izinkan sertifikat TLS proxy Instagram yang tidak valid. Aktifkan hanya pada jaringan tepercaya.',instagramProxyForceHTTP2:'Paksa koneksi proxy Instagram memakai HTTP/2 bila proxy mendukungnya.',flickrApiKey:'API key resmi Flickr yang digunakan untuk mengambil media dari tautan Flickr.',
      admins:'Daftar Discord User ID yang diizinkan menjalankan command administratif.',adminChannels:'Channel tempat log/status admin dikirim dan command dapat dibuka secara khusus.',
      guiAddress:'Alamat host:port untuk dashboard. Demi keamanan harus localhost atau alamat loopback.',guiAutoStart:'Jika mati, mode GUI hanya membuka dashboard lalu menunggu tombol Start. Mode CLI tetap langsung berjalan.',guiCloseToTray:'Saat tombol tutup ditekan, tampilkan pilihan jelas untuk menyembunyikan aplikasi tanpa menghentikan unduhan, keluar dengan aman, atau batal.',processLimit:'Batas umum pekerjaan paralel agar CPU dan koneksi tidak kewalahan.',
      downloadWorkers:'Jumlah unduhan real-time yang dapat diproses paralel. Naikkan bertahap sesuai bandwidth dan disk.',downloadQueueSize:'Jumlah request yang dapat menunggu; nilainya tidak boleh lebih kecil dari jumlah worker.',
      downloadMaxFileSize:'Batas respons HTTP per file. Editor menampilkan nilai dalam MB tetapi menyimpan byte.',downloadRetryMax:'Total percobaan ketika transfer gagal sementara.',downloadMaxRedirects:'Batas redirect HTTP untuk mencegah loop dan tujuan tidak terduga.',
      historyRequestCount:'Jumlah pesan Discord per halaman, antara 1 dan 100.',historyRequestDelay:'Jeda tambahan antar-halaman history untuk mengurangi tekanan rate limit.',historyMaxJobs:'Jumlah channel history yang boleh dipindai secara bersamaan.',
      filenameFormat:'Template nama output; dapat memakai placeholder seperti {{date}} dan {{file}}.',filenameDateFormat:'Format waktu Go yang dipakai untuk placeholder tanggal pada nama file.',subfolders:'Urutan template folder relatif, misalnya {{fileType}}.',
      filters:'Aturan allowed/blocked untuk frase, user, role, domain, ekstensi, nama file, isi link, dan reaksi.',duplo:'Aktifkan pencarian gambar yang sangat mirip untuk menekan duplikasi visual.',duploThreshold:'Tingkat kemiripan Duplo; ubah hanya jika memahami trade-off false positive.',
      all:'Template sumber global yang berlaku sebelum aturan target yang lebih spesifik.',users:'Daftar aturan arsip khusus Discord User ID.',servers:'Daftar aturan untuk seluruh server. Satu Server ID sudah mencakup semua channel yang dapat dilihat bot.',categories:'Daftar aturan arsip khusus kategori channel.',channels:'Daftar channel tertentu. Channel dapat ditambahkan langsung tanpa membuat entri server.',server:'ID server Discord yang disalin dari menu klik kanan server.',channel:'ID channel Discord yang disalin dari menu klik kanan channel.',category:'ID kategori Discord yang menaungi beberapa channel.',user:'ID pengguna Discord untuk aturan berbasis pengirim.',destination:'Folder Windows tempat media sumber ini disimpan, misalnya D:\\Discord Archive\\Server Saya.',
      presenceStatus:'Status yang terlihat di Discord: online, idle, dnd, atau invisible.',presenceType:'Jenis aktivitas Discord seperti Playing, Streaming, Listening, atau Watching.',
      sendFileDirectly:'Jika aktif, file diteruskan langsung; jika mati aplikasi dapat memakai perilaku forwarding alternatif.',logOutput:'Path file log. Kosong berarti output tetap mengikuti console.',
      _constants:'Pasangan nama dan nilai yang dapat digunakan kembali dalam template konfigurasi.'
    };
    const settingDescriptionsEN={
      token:'Bot token from the Discord Developer Portal. Stored values are hidden and remain unchanged when this field is blank.',email:'Discord email for the legacy email login method only. Normal bots should use a bot token. Stored values are hidden.',password:'Discord password paired with the legacy Discord email login; this is not the Instagram password. Leave blank to preserve it.',
      twitterUsername:'Twitter/X account name used by the media resolver.',twitterPassword:'Password for the Twitter/X account; separate from Discord and Instagram credentials.',twitter2FA:'Two-factor authentication code or secret for Twitter/X.',twitterAuthToken:'Twitter/X auth_token cookie for an authenticated session.',twitterCT0:'Twitter/X CSRF ct0 cookie paired with auth_token.',
      instagramUsername:'Instagram account name used by the media resolver; this is not a Discord ID.',instagramPassword:'Password for the Instagram account; separate from the legacy Discord password.',instagramTOTP:'Instagram TOTP secret used to generate two-factor authentication codes.',instagramProxy:'Proxy used only for Instagram connections.',instagramProxyInsecure:'Allow an Instagram proxy with invalid TLS certificates. Use only on a trusted network.',instagramProxyForceHTTP2:'Force the Instagram proxy connection to use HTTP/2 when supported.',flickrApiKey:'Official Flickr API key used to retrieve media from Flickr links.',
      admins:'Discord user IDs allowed to run administrative commands.',adminChannels:'Channels used for administrator logs, status, and optionally unlocked commands.',guiAddress:'Dashboard host:port. For security, it must remain on localhost or another loopback address.',guiAutoStart:'When off, GUI mode opens only the dashboard and waits for Start. CLI mode still starts immediately.',guiCloseToTray:'When the close button is pressed, show clear choices to hide without stopping downloads, exit safely, or cancel.',processLimit:'General parallel task limit used to protect CPU and network resources.',
      downloadWorkers:'Number of real-time downloads processed in parallel. Increase gradually based on network and disk capacity.',downloadQueueSize:'Number of requests allowed to wait; it cannot be smaller than the worker count.',downloadMaxFileSize:'Maximum HTTP response size per file. The editor shows MB while the configuration stores bytes.',downloadRetryMax:'Number of attempts made after a temporary transfer failure.',downloadMaxRedirects:'HTTP redirect limit that prevents loops and unexpected destinations.',
      historyRequestCount:'Number of Discord messages fetched per page, from 1 to 100.',historyRequestDelay:'Additional delay between history pages to reduce rate-limit pressure.',historyMaxJobs:'Number of channel history scans allowed to run concurrently.',filenameFormat:'Output filename template; placeholders such as {{date}} and {{file}} are supported.',filenameDateFormat:'Go time format used for the date placeholder in filenames.',subfolders:'Ordered relative folder templates, for example {{fileType}}.',
      filters:'Allow/block rules for phrases, users, roles, domains, extensions, filenames, link contents, and reactions.',duplo:'Detect visually similar images to reduce duplicate media.',duploThreshold:'Duplo similarity threshold; change only when you understand the false-positive tradeoff.',all:'Global source template applied before more specific target rules.',users:'Archive rules for specific Discord users.',servers:'Rules for entire Discord servers. One Server ID covers every channel visible to the bot.',categories:'Archive rules for specific channel categories.',channels:'Specific channel rules. A channel can be added directly without creating a server entry.',server:'Discord Server ID copied from the server context menu.',channel:'Discord Channel ID copied from the channel context menu.',category:'Discord Category ID containing one or more channels.',user:'Discord User ID used for sender-based rules.',destination:'Windows folder where media from this source is stored, for example D:\\Discord Archive\\My Server.',presenceStatus:'Discord presence: online, idle, dnd, or invisible.',presenceType:'Discord activity type such as Playing, Streaming, Listening, or Watching.',sendFileDirectly:'Forward files directly when enabled; otherwise use the configured alternative forwarding behavior.',logOutput:'Log file path. Leave blank to continue using console output.',_constants:'Reusable name/value pairs for configuration templates.'
    };
    const credentialOrder=['token','email','password','twitterEnabled','twitterUsername','twitterPassword','twitter2FA','twitterAuthToken','twitterCT0','twitterProxy','instagramEnabled','instagramUsername','instagramPassword','instagramTOTP','instagramProxy','instagramProxyInsecure','instagramProxyForceHTTP2','flickrApiKey'];
    const sourceOrder=['user','users','server','servers','serverBlacklist','category','categories','categoryBlacklist','channel','channels','destination','alias','aliases','enabled','save','allowCommands','scanEdits','ignoreBots','commandPrefix','ignoreEmojis','ignoreStickers'];
    function categoryFor(key){return settingsCategories.find((category)=>category.keys.includes(key))||settingsCategories[settingsCategories.length-1];}
    function orderedEntries(value,path){const order=path[0]==='credentials'?credentialOrder:sourceOrder;let entries=Object.entries(value);if(settingsMode==='normal'&&path.length===1&&normalNestedKeys[path[0]])entries=entries.filter(([key])=>normalNestedKeys[path[0]].has(key));return entries.sort(([left],[right])=>{const a=order.indexOf(left),b=order.indexOf(right);if(a<0&&b<0)return left.localeCompare(right);if(a<0)return 1;if(b<0)return -1;return a-b;});}
    function isSourceOverride(path){return ['all','users','servers','categories','channels'].includes(String(path[0]))&&path.length>1;}
    function getSetting(path,root=settingsModel){let value=root;for(const part of path){if(value==null)return undefined;value=value[part];}return value;}
    function getTemplate(path){let value=settingsTemplates;for(const part of path){if(value==null)return undefined;value=Array.isArray(value)?value[0]:value[part];}return value;}
    function setSetting(path,value,rerender=false){let target=settingsModel;for(let i=0;i<path.length-1;i++)target=target[path[i]];target[path[path.length-1]]=value;updateSettingsState();if(rerender)renderSettings();}
    function friendlyLabel(name){return currentLanguage==='en'?(settingLabelsEN[name]||optionLabel(name)):(settingLabels[name]||optionLabel(name));}
    function settingHelpEnglish(name,path){
      if(settingDescriptionsEN[name])return settingDescriptionsEN[name];
      if(/^blocked/i.test(name))return 'Values rejected within this scope. Matching items are excluded from the archive.';
      if(/^allowed/i.test(name))return 'Allowed values for this scope. When populated, only matching items are accepted.';
      if(/^save/i.test(name))return 'Choose whether this data type is saved within the selected scope.';
      if(/^ignore/i.test(name))return 'When enabled, this message or media type is skipped during scanning.';
      if(/destination/i.test(name))return 'Custom output folder. Blank or null values inherit the parent destination.';
      if(/filename/i.test(name))return 'Template controlling the output filename without altering the media contents.';
      if(/timeout/i.test(name))return 'Maximum time to wait before the operation is treated as failed.';
      if(/rate|delay/i.test(name))return 'Operation interval used to maintain stability and respect rate limits.';
      if(/channel|server|category|user/i.test(name))return 'Discord ID used to select the target or scope of this rule.';
      if(/enabled/i.test(name))return 'Enable or disable this feature without deleting its related configuration.';
      if(/output|log|verbose|debug/i.test(name))return 'Controls diagnostic information written by the application. Higher detail may produce larger logs.';
      if(/proxy/i.test(name))return 'Proxy address for this service. Use only a proxy you trust.';
      return 'Complete value for '+friendlyLabel(name)+'. It is stored in the settings file and some changes require an application restart.';
    }
    function settingHelp(name,path){
      if(currentLanguage==='en')return settingHelpEnglish(name,path);
      if(settingDescriptions[name])return settingDescriptions[name];
      if(/^blocked/i.test(name))return 'Daftar nilai yang ditolak untuk cakupan ini. Pencocokan digunakan untuk mencegah media yang tidak diinginkan.';
      if(/^allowed/i.test(name))return 'Daftar nilai yang diizinkan. Jika diisi, hanya item yang cocok dengan daftar ini yang diterima.';
      if(/^save/i.test(name))return 'Tentukan apakah jenis data ini disimpan untuk cakupan yang dipilih.';
      if(/^ignore/i.test(name))return 'Jika aktif, jenis pesan atau media ini dilewati saat pemindaian.';
      if(/destination/i.test(name))return 'Folder output khusus. Kosong atau null akan mengikuti aturan tujuan induk.';
      if(/filename/i.test(name))return 'Template yang mengontrol nama file hasil tanpa mengubah isi media.';
      if(/timeout/i.test(name))return 'Waktu maksimum menunggu operasi sebelum dianggap gagal.';
      if(/rate|delay/i.test(name))return 'Interval pengendali frekuensi operasi untuk menjaga stabilitas dan rate limit.';
      if(/channel|server|category|user/i.test(name))return 'Discord ID untuk menentukan target atau cakupan aturan ini.';
      if(/enabled/i.test(name))return 'Aktifkan atau nonaktifkan fitur ini tanpa menghapus konfigurasi terkait.';
      if(/output|log|verbose|debug/i.test(name))return 'Mengontrol informasi diagnosis yang ditulis aplikasi. Detail tinggi dapat menghasilkan log lebih besar.';
      if(/proxy/i.test(name))return 'Alamat proxy untuk koneksi layanan ini. Gunakan hanya proxy yang dipercaya.';
      return 'Nilai lengkap untuk '+friendlyLabel(name)+'. Perubahan disimpan ke file konfigurasi dan sebagian opsi memerlukan restart aplikasi.';
    }
    function displayHelp(name,path){return settingHelp(name,path)+(isSourceOverride(path)?tx('overrideHelp'):'');}
    function fieldSearch(name,value,path,template){return optionSearch(pathText(path)+' '+friendlyLabel(name)+' '+displayHelp(name,path)+' '+JSON.stringify(value)+' '+JSON.stringify(template));}
    function fieldHeading(name,path){const head=document.createElement('div');head.className='field-heading';const label=document.createElement('label');label.textContent=friendlyLabel(name);const meta=document.createElement('span');meta.className='field-path';meta.textContent=pathText(path);if(isSourceOverride(path)){const badge=document.createElement('b');badge.className='scope-badge';badge.textContent='override';meta.prepend(badge);}head.append(label,meta);return head;}
    function enumOptions(name){if(name==='presenceStatus')return [['online','Online'],['idle','Idle'],['dnd','Do not disturb'],['invisible','Invisible']];if(name==='presenceType')return [[0,'Playing'],[1,'Streaming'],[2,'Listening'],[3,'Watching'],[4,'Custom'],[5,'Competing']];return null;}
    function numericRules(name){const rules={downloadWorkers:[1,null,1],downloadQueueSize:[1,null,1],downloadTimeout:[10,null,1],discordTimeout:[10,null,1],downloadRetryMax:[1,null,1],downloadMaxRedirects:[0,null,1],historyMaxJobs:[1,null,1],historyRequestCount:[1,100,1],downloadMaxFileSize:[1,null,1]};return rules[name]||[null,null,Number.isInteger(getSetting([name]))?1:'any'];}
    function scalarControl(name,value,path){
      const enums=enumOptions(name),control=document.createElement('div');control.className='field-control';
      if(typeof value==='boolean'){
        const toggle=document.createElement('label');toggle.className='toggle-control';const input=document.createElement('input');input.type='checkbox';input.checked=value;const track=document.createElement('span');track.className='toggle-track';const state=document.createElement('span');state.className='toggle-state';state.textContent=value?tx('enabled'):tx('disabled');input.addEventListener('change',()=>{state.textContent=input.checked?tx('enabled'):tx('disabled');setSetting(path,input.checked);});toggle.append(input,track,state);control.appendChild(toggle);return control;
      }
      if(enums){const select=document.createElement('select');enums.forEach(([option,label])=>{const item=document.createElement('option');item.value=option;item.textContent=label;item.selected=String(value)===String(option);select.appendChild(item);});select.addEventListener('change',()=>setSetting(path,typeof value==='number'?Number(select.value):select.value));control.appendChild(select);return control;}
      const input=document.createElement('input');const secret=/token|password|totp|api.?key|ct0|proxy/i.test(name);input.type=typeof value==='number'?'number':(secret?'password':'text');let displayValue=value??'';let multiplier=1,unit='';if(name==='downloadMaxFileSize'){displayValue=Math.round(Number(value)/1048576);multiplier=1048576;unit='MB';}
      input.value=displayValue;if(typeof value==='number'){const [min,max,step]=numericRules(name);if(min!==null)input.min=min;if(max!==null)input.max=max;input.step=step;}if(settingsSecretFields[pathText(path)])input.placeholder=tx('configuredSecret');
      input.addEventListener('input',()=>setSetting(path,typeof value==='number'?Number(input.value)*multiplier:input.value));control.appendChild(input);if(unit){const suffix=document.createElement('span');suffix.className='field-unit';suffix.textContent=unit;control.appendChild(suffix);}return control;
    }
    function optionalSettingField(name,value,path,template){
      if(template!==null&&typeof template==='object'){
        const wrap=document.createElement('div');wrap.className='setting-collection setting-search';wrap.dataset.search=fieldSearch(name,value,path,template);const head=document.createElement('div');head.className='collection-head';const info=document.createElement('div');info.innerHTML='<h4>'+escapeHtml(friendlyLabel(name))+'</h4><p>'+escapeHtml(displayHelp(name,path))+'</p>';const use=document.createElement('button');use.type='button';use.className='secondary';use.textContent=tx('activateConfig');use.addEventListener('click',()=>setSetting(path,cloneValue(template),true));head.append(info,use);wrap.appendChild(head);return wrap;
      }
      const wrap=document.createElement('div');wrap.className='setting-field setting-search';wrap.dataset.search=fieldSearch(name,value,path,template);wrap.appendChild(fieldHeading(name,path));const help=document.createElement('p');help.className='field-help';help.textContent=displayHelp(name,path)+tx('inheritHelp');wrap.appendChild(help);
      const group=document.createElement('div');group.className='optional-control inherited';const mode=document.createElement('select');[['inherit',tx('inherit')],['custom',tx('customValue')]].forEach(([key,label])=>{const option=document.createElement('option');option.value=key;option.textContent=label;mode.appendChild(option);});const placeholder=document.createElement('div');placeholder.appendChild(scalarControl(name,template,path));placeholder.querySelectorAll('input,select,button').forEach((control)=>control.disabled=true);mode.addEventListener('change',()=>setSetting(path,mode.value==='inherit'?null:cloneValue(template),true));group.append(mode,placeholder);wrap.appendChild(group);return wrap;
    }
    function settingField(name,value,path,depth,template){
      const search=fieldSearch(name,value,path,template);
      if(value===null)return optionalSettingField(name,value,path,template);
      if(Array.isArray(value))return arraySettingField(name,value,path,template);
      if(typeof value==='object'){
        if(Object.keys(value).length===0)return jsonSettingField(name,value,path,search,template);
        const entries=orderedEntries(value,path),details=document.createElement('details');details.className='setting-object setting-search';details.dataset.search=search;details.open=depth===0&&name==='credentials';const summary=document.createElement('summary');summary.innerHTML='<span>'+escapeHtml(friendlyLabel(name))+'</span><small>'+entries.length+' '+bi('opsi','options')+' · '+escapeHtml(displayHelp(name,path))+'</small>';details.appendChild(summary);const body=document.createElement('div');body.className='setting-object-body';entries.forEach(([key,item])=>body.appendChild(settingField(key,item,path.concat(key),depth+1,template?.[key])));details.appendChild(body);return details;
      }
      const wrap=document.createElement('div');wrap.className='setting-field setting-search';wrap.dataset.search=search;wrap.dataset.fieldPath=pathText(path);wrap.appendChild(fieldHeading(name,path));const help=document.createElement('p');help.className='field-help';help.textContent=displayHelp(name,path);wrap.append(help,scalarControl(name,value,path));return wrap;
    }
    function sourceEntryFor(name){const idKey={users:'user',servers:'server',categories:'category',channels:'channel'}[name];return idKey?{[idKey]:'',destination:'',enabled:true,save:true}:cloneValue(Array.isArray(settingsTemplates?.[name])?settingsTemplates[name][0]:'');}
    function addSourceEntry(name){if(!Array.isArray(settingsModel[name]))settingsModel[name]=[];const entry=sourceEntryFor(name),index=settingsModel[name].push(entry)-1;lastAddedSource={name,index};updateSettingsState();renderSettings();setTimeout(()=>{const idKey={users:'user',servers:'server',categories:'category',channels:'channel'}[name],field=document.querySelector('[data-field-path="'+name+'.'+index+'.'+idKey+'"] input');if(field){field.scrollIntoView({behavior:'smooth',block:'center'});field.focus();}},0);}
    function builderField(label,value,placeholder=''){const wrap=document.createElement('div');wrap.className='server-field';const caption=document.createElement('label');caption.textContent=label;const input=document.createElement('input');input.type='text';input.value=value??'';input.placeholder=placeholder;wrap.append(caption,input);return {wrap,input};}
    function validSnowflake(value){return /^\d{15,22}$/.test(String(value||'').trim());}
    function automaticChannelSubfolders(){return ['{{channelName}}','{{fileType}}'];}
    function usesAutomaticChannelFolder(item,serverDestination){return Array.isArray(item?.subfolders)?item.subfolders.includes('{{channelName}}'):item?.destination===serverDestination;}
    function enableAutomaticChannelFolder(item,serverDestination){item.destination=serverDestination;item.subfolders=automaticChannelSubfolders();}
    function disableAutomaticChannelFolder(item){delete item.subfolders;}
    function addServerGroup(){if(!Array.isArray(settingsModel.servers))settingsModel.servers=[];const index=settingsModel.servers.push({server:'',destination:'',subfolders:automaticChannelSubfolders(),enabled:false,save:false})-1;lastAddedSource={name:'servers',index};updateSettingsState();renderSettings();setTimeout(()=>document.querySelector('[data-server-index="'+index+'"] input')?.focus(),0);}
    function renderSourceGuide(){
      const guide=document.createElement('section');guide.className='source-guide';guide.id='sourceSetupGuide';const hasPlaceholder=['servers','channels'].some((name)=>(settingsModel[name]||[]).some((item)=>Object.values(item||{}).some((value)=>typeof value==='string'&&value.startsWith('REPLACE_WITH_'))));
      guide.innerHTML='<div class="source-guide-head"><h4>'+escapeHtml(bi('Server → channel → penyimpanan','Server → channels → storage'))+'</h4><p>'+escapeHtml(bi('Buat satu kartu untuk setiap server. Di dalamnya kamu bisa mengarsipkan seluruh server atau hanya menambahkan channel tertentu, dengan folder custom per server maupun per channel.','Create one card for each server. Inside it, archive the entire server or add selected channels only, with custom storage per server or per channel.'))+'</p></div><div class="server-builder-head"><p>'+escapeHtml(bi('Punya 6 server? Klik “Tambah server” enam kali; setiap server mendapat kartu dan daftar channel sendiri.','Have 6 servers? Click “Add server” six times; each server gets its own card and channel list.'))+'</p><button id="addServerGroup" type="button">'+escapeHtml(bi('+ Tambah server','+ Add server'))+'</button></div><div class="server-builder" id="serverBuilder"></div><div class="source-priority">'+escapeHtml(bi('Jika “Arsip semua channel” mati, server hanya menjadi grup dan hanya channel di dalam daftar yang diarsipkan. Jika aktif, seluruh server diarsipkan dan channel anak dapat memiliki pengaturan khusus.','When “Archive every channel” is off, the server is only a group and only listed channels are archived. When on, the entire server is archived and child channels may use custom settings.'))+'</div>'+(hasPlaceholder?'<div class="source-warning">'+escapeHtml(bi('Masih ada entri contoh REPLACE_WITH_... di bagian channel tanpa grup. Hapus atau ganti dengan ID dan folder asli.','A REPLACE_WITH_... sample remains under ungrouped channels. Remove it or replace it with a real ID and folder.'))+'</div>':'');
      guide.querySelector('#addServerGroup').addEventListener('click',addServerGroup);const builder=guide.querySelector('#serverBuilder'),servers=settingsModel.servers||[],channels=settingsModel.channels||[];
      if(!servers.length){const empty=document.createElement('div');empty.className='server-builder-empty';empty.textContent=bi('Belum ada server. Klik “Tambah server” untuk membuat Server 1.','No servers yet. Click “Add server” to create Server 1.');builder.appendChild(empty);}
      servers.forEach((server,serverIndex)=>{
        if(!Array.isArray(server.subfolders))server.subfolders=automaticChannelSubfolders();
        const card=document.createElement('article');card.className='server-group';card.dataset.serverIndex=serverIndex;const head=document.createElement('div');head.className='server-group-head';const title=document.createElement('strong');title.textContent=bi('Server ','Server ')+(serverIndex+1);const removeServer=document.createElement('button');removeServer.type='button';removeServer.className='danger';removeServer.textContent=bi('Hapus server','Remove server');head.append(title,removeServer);card.appendChild(head);
        const fields=document.createElement('div');fields.className='server-fields';const idField=builderField('SERVER ID',server.server,bi('Tempel Server ID','Paste Server ID')),storageField=builderField(bi('FOLDER PENYIMPANAN SERVER','SERVER STORAGE FOLDER'),server.destination,bi('Contoh: D:\\Discord Archive\\Server 1','Example: D:\\Discord Archive\\Server 1'));const storageHint=document.createElement('small');storageHint.className='server-storage-hint';storageHint.textContent=bi('Otomatis menjadi: folder server \\ nama channel \\ images/videos','Automatic result: server folder \\ channel name \\ images/videos');storageField.wrap.appendChild(storageHint);fields.append(idField.wrap,storageField.wrap);card.appendChild(fields);
        const mode=document.createElement('label');mode.className='server-mode';const modeText=document.createElement('div');modeText.innerHTML='<strong>'+escapeHtml(bi('Arsip semua channel di server ini','Archive every channel in this server'))+'</strong><small>'+escapeHtml(bi('Matikan jika hanya ingin channel yang ditambahkan di bawah.','Turn off to archive only the channels listed below.'))+'</small>';const modeToggle=document.createElement('input');modeToggle.type='checkbox';modeToggle.checked=server.enabled!==false&&server.save!==false;mode.append(modeText,modeToggle);card.appendChild(mode);
        const channelBox=document.createElement('div');channelBox.className='server-channels';const channelHead=document.createElement('div');channelHead.className='server-channels-head';const serverChannels=channels.map((item,index)=>({item,index})).filter(({item})=>configuredText(server.server)&&item?.server===server.server);const channelTitle=document.createElement('span');channelTitle.textContent=bi('Channel dalam server ini','Channels in this server')+' · '+serverChannels.length;const addChannel=document.createElement('button');addChannel.type='button';addChannel.className='secondary';addChannel.textContent=bi('+ Tambah channel','+ Add channel');channelHead.append(channelTitle,addChannel);channelBox.appendChild(channelHead);
        if(!serverChannels.length){const empty=document.createElement('div');empty.className='collection-empty';empty.textContent=bi('Belum ada channel khusus. Aktifkan arsip semua channel atau tambahkan channel di sini.','No specific channels yet. Enable full-server archiving or add channels here.');channelBox.appendChild(empty);}
        serverChannels.forEach(({item,index})=>{const row=document.createElement('div');row.className='channel-row';row.dataset.channelIndex=index;if(!Array.isArray(item.subfolders)&&item.destination===server.destination)item.subfolders=automaticChannelSubfolders();let automatic=usesAutomaticChannelFolder(item,server.destination);const channelID=builderField('CHANNEL ID',item.channel,bi('Tempel Channel ID','Paste Channel ID')),channelStorage=builderField(bi('FOLDER DASAR / FOLDER CUSTOM CHANNEL','CHANNEL BASE / CUSTOM FOLDER'),item.destination,server.destination||bi('Isi folder tujuan','Enter destination folder'));channelID.wrap.className='channel-field';channelStorage.wrap.className='channel-field';const storageActions=document.createElement('div');storageActions.className='channel-storage-actions';const inherit=document.createElement('button');inherit.type='button';inherit.className='secondary';inherit.textContent=bi('Otomatis: server/channel','Automatic: server/channel');const storageState=document.createElement('span');storageState.className='channel-storage-state'+(automatic?' auto':'');storageState.textContent=automatic?bi('AKTIF · ditambah nama channel','ON · channel name appended'):bi('CUSTOM · alamat dipakai langsung','CUSTOM · exact path used');inherit.addEventListener('click',()=>{enableAutomaticChannelFolder(item,server.destination);channelStorage.input.value=server.destination;storageState.className='channel-storage-state auto';storageState.textContent=bi('AKTIF · ditambah nama channel','ON · channel name appended');updateSettingsState();});storageActions.append(inherit,storageState);channelStorage.wrap.appendChild(storageActions);const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent=bi('Hapus','Remove');channelID.input.addEventListener('input',()=>{item.channel=channelID.input.value.trim();updateSettingsState();});channelStorage.input.addEventListener('input',()=>{item.destination=channelStorage.input.value;disableAutomaticChannelFolder(item);storageState.className='channel-storage-state';storageState.textContent=bi('CUSTOM · alamat dipakai langsung','CUSTOM · exact path used');updateSettingsState();});remove.addEventListener('click',()=>{settingsModel.channels.splice(index,1);updateSettingsState();renderSettings();});row.append(channelID.wrap,channelStorage.wrap,remove);channelBox.appendChild(row);});card.appendChild(channelBox);
        idField.input.addEventListener('input',()=>{const previous=server.server;server.server=idField.input.value.trim();channels.forEach((item)=>{if(configuredText(previous)&&item.server===previous)item.server=server.server;});updateSettingsState();});storageField.input.addEventListener('input',()=>{const previous=server.destination;server.destination=storageField.input.value;channels.forEach((item)=>{if(item.server===server.server&&(!configuredText(item.destination)||item.destination===previous))item.destination=server.destination;});updateSettingsState();});modeToggle.addEventListener('change',()=>{server.enabled=modeToggle.checked;server.save=modeToggle.checked;updateSettingsState();});
        addChannel.addEventListener('click',()=>{if(!validSnowflake(server.server)){toast(bi('Isi Server ID yang valid terlebih dahulu.','Enter a valid Server ID first.'),true);idField.input.focus();return;}if(!configuredText(server.destination)){toast(bi('Isi folder penyimpanan server terlebih dahulu.','Enter the server storage folder first.'),true);storageField.input.focus();return;}const index=settingsModel.channels.push({server:server.server,channel:'',destination:server.destination,subfolders:automaticChannelSubfolders(),enabled:true,save:true})-1;lastAddedSource={name:'channels',index};updateSettingsState();renderSettings();setTimeout(()=>document.querySelector('[data-channel-index="'+index+'"] input')?.focus(),0);});
        removeServer.addEventListener('click',()=>{const childCount=channels.filter((item)=>configuredText(server.server)&&item.server===server.server).length;if(!confirm(bi('Hapus Server '+(serverIndex+1)+' dan '+childCount+' channel anaknya?','Remove Server '+(serverIndex+1)+' and its '+childCount+' child channel(s)?')))return;settingsModel.servers.splice(serverIndex,1);settingsModel.channels=settingsModel.channels.filter((item)=>!configuredText(server.server)||item.server!==server.server);updateSettingsState();renderSettings();});builder.appendChild(card);
      });
      const knownServers=new Set(servers.map((server)=>server.server).filter(configuredText)),standalone=channels.map((item,index)=>({item,index})).filter(({item})=>!configuredText(item?.server)||!knownServers.has(item.server));if(standalone.length){const box=document.createElement('div');box.className='standalone-channels';const head=document.createElement('div');head.className='server-channels-head';head.innerHTML='<span>'+escapeHtml(bi('Channel tanpa grup server','Ungrouped channels'))+' · '+standalone.length+'</span>';box.appendChild(head);standalone.forEach(({item,index})=>{const row=document.createElement('div');row.className='channel-row';const channelID=builderField('CHANNEL ID',item.channel,''),storage=builderField(bi('FOLDER PENYIMPANAN','STORAGE FOLDER'),item.destination,'');channelID.wrap.className='channel-field';storage.wrap.className='channel-field';const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent=bi('Hapus','Remove');channelID.input.addEventListener('input',()=>{item.channel=channelID.input.value.trim();updateSettingsState();});storage.input.addEventListener('input',()=>{item.destination=storage.input.value;updateSettingsState();});remove.addEventListener('click',()=>{settingsModel.channels.splice(index,1);updateSettingsState();renderSettings();});row.append(channelID.wrap,storage.wrap,remove);box.appendChild(row);});builder.appendChild(box);}return guide;
    }
    function arraySettingField(name,value,path,template){
      const wrap=document.createElement('div');wrap.className='setting-collection setting-search';wrap.dataset.search=fieldSearch(name,value,path,template);wrap.dataset.settingRoot=name;const head=document.createElement('div');head.className='collection-head';const info=document.createElement('div');info.innerHTML='<h4>'+escapeHtml(friendlyLabel(name))+' <span class="count-chip">'+value.length+'</span></h4><p>'+escapeHtml(displayHelp(name,path))+'</p>';const add=document.createElement('button');add.type='button';add.className='secondary';add.textContent=tx('add');add.addEventListener('click',()=>{if(['users','servers','categories','channels'].includes(name)&&path.length===1){addSourceEntry(name);return;}value.push(cloneValue(Array.isArray(template)?template[0]:''));updateSettingsState();renderSettings();});head.append(info,add);wrap.appendChild(head);
      const body=document.createElement('div');body.className='collection-body';if(!value.length){const empty=document.createElement('div');empty.className='collection-empty';empty.textContent=tx('emptyList');body.appendChild(empty);}
      value.forEach((item,index)=>{if(item!==null&&typeof item==='object'&&!Array.isArray(item)){const card=document.createElement('details');card.className='collection-item';card.open=index===0||(lastAddedSource?.name===name&&lastAddedSource?.index===index);const summary=document.createElement('summary');summary.className='collection-item-head';summary.textContent=friendlyLabel(name)+' #'+(index+1);card.appendChild(summary);const fields=document.createElement('div');fields.className='collection-fields';orderedEntries(item,path).forEach(([key,entry])=>fields.appendChild(settingField(key,entry,path.concat(index,key),2,(Array.isArray(template)?template[0]:template)?.[key])));const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent=tx('removeEntry',{n:index+1});remove.addEventListener('click',()=>{value.splice(index,1);updateSettingsState();renderSettings();});const actions=document.createElement('div');actions.style.padding='0 9px 9px';actions.appendChild(remove);card.append(fields,actions);body.appendChild(card);}else{const row=document.createElement('div');row.className='collection-item primitive-row';const input=document.createElement('input');input.type=typeof item==='number'?'number':'text';input.value=item??'';input.addEventListener('input',()=>{value[index]=typeof item==='number'?Number(input.value):input.value;updateSettingsState();});const remove=document.createElement('button');remove.type='button';remove.className='danger';remove.textContent=tx('remove');remove.addEventListener('click',()=>{value.splice(index,1);updateSettingsState();renderSettings();});row.append(input,remove);body.appendChild(row);}});wrap.appendChild(body);return wrap;
    }
    function jsonSettingField(name,value,path,search,template){const wrap=document.createElement('div');wrap.className='setting-field wide setting-search';wrap.dataset.search=search;wrap.appendChild(fieldHeading(name,path));const help=document.createElement('p');help.className='field-help';help.textContent=displayHelp(name,path);const area=document.createElement('textarea');area.value=JSON.stringify(value,null,2);area.dataset.settingPath=JSON.stringify(path);area.addEventListener('input',()=>{try{const parsed=JSON.parse(area.value);area.setCustomValidity('');setSetting(path,parsed);}catch(error){area.setCustomValidity(error.message);updateValidation();}});wrap.append(help,area);const warning=document.createElement('div');warning.className='json-warning';warning.textContent=tx('dynamicJSON');wrap.appendChild(warning);return wrap;}
    function commitJSONSettings(){for(const area of document.querySelectorAll('[data-setting-path]')){let parsed;try{parsed=JSON.parse(area.value);}catch(error){area.focus();throw new Error(pathText(JSON.parse(area.dataset.settingPath))+': '+error.message);}let target=settingsModel,path=JSON.parse(area.dataset.settingPath);for(let i=0;i<path.length-1;i++)target=target[path[i]];target[path[path.length-1]]=parsed;}}
    function eligibleSettingsKeys(){const keys=Object.keys(settingsModel||{});return settingsMode==='advanced'?keys:keys.filter((key)=>normalSettingKeys.has(key));}
    function fieldsForCategory(id){const keys=eligibleSettingsKeys();if(id==='search'){const query=optionSearch(byId('settingsSearch').value);return keys.filter((key)=>fieldSearch(key,settingsModel[key],[key],settingsTemplates?.[key]).includes(query));}const category=settingsCategories.find((item)=>item.id===id);return keys.filter((key)=>categoryFor(key).id===id).sort((left,right)=>{const a=category?.keys.indexOf(left)??-1,b=category?.keys.indexOf(right)??-1;if(a<0&&b<0)return left.localeCompare(right);if(a<0)return 1;if(b<0)return -1;return a-b;});}
    function categoryName(category){return currentLanguage==='en'?category.labelEn:category.label;}
    function categoryDescription(category){return currentLanguage==='en'?category.descriptionEn:category.description;}
    function renderSettingsNav(){const nav=byId('settingsNav');nav.innerHTML='';settingsCategories.forEach((category)=>{const count=fieldsForCategory(category.id).length;if(!count)return;const button=document.createElement('button');button.type='button';button.className=activeSettingsCategory===category.id?'active':'';button.setAttribute('aria-pressed',activeSettingsCategory===category.id);button.innerHTML='<span class="nav-icon">'+category.icon+'</span><span class="nav-label">'+escapeHtml(categoryName(category))+'</span><span class="nav-count">'+count+'</span>';button.addEventListener('click',()=>{activeSettingsCategory=category.id;lastSettingsCategory=category.id;byId('settingsSearch').value='';renderSettings();});nav.appendChild(button);});}
    function renderSettings(){const editor=byId('settingsEditor');editor.innerHTML='';if(!settingsModel){editor.innerHTML='<div class="settings-empty">'+escapeHtml(tx('noSettings'))+'</div>';return;}const fields=fieldsForCategory(activeSettingsCategory),category=activeSettingsCategory==='search'?{label:tx('searchResults'),labelEn:tx('searchResults'),description:tx('searchResultsHelp'),descriptionEn:tx('searchResultsHelp')}:(settingsCategories.find((item)=>item.id===activeSettingsCategory)||settingsCategories[0]);byId('settingsCategoryTitle').textContent=categoryName(category);byId('settingsCategoryDesc').textContent=categoryDescription(category);byId('settingsCount').textContent=fields.length;renderSettingsNav();if(!fields.length){editor.innerHTML='<div class="settings-empty">'+escapeHtml(tx('noMatchingSettings'))+'</div>';return;}if(activeSettingsCategory==='sources')editor.appendChild(renderSourceGuide());const grid=document.createElement('div');grid.className='category-grid';const visibleFields=activeSettingsCategory==='sources'?fields.filter((key)=>!['servers','channels'].includes(key)):fields;visibleFields.forEach((key)=>grid.appendChild(settingField(key,settingsModel[key],[key],0,settingsTemplates?.[key])));editor.appendChild(grid);}
    function setSettingsMode(mode,persist=true){settingsMode=mode==='advanced'?'advanced':'normal';const advanced=settingsMode==='advanced';const normalButton=byId('settingsModeNormal'),advancedButton=byId('settingsModeAdvanced');normalButton.classList.toggle('active',!advanced);normalButton.setAttribute('aria-pressed',!advanced);advancedButton.classList.toggle('active',advanced);advancedButton.setAttribute('aria-pressed',advanced);byId('settingsModeName').dataset.i18n=advanced?'advancedMode':'normalMode';byId('settingsModeHelp').dataset.i18n=advanced?'advancedModeHelp':'normalModeHelp';byId('settingsModeName').textContent=tx(advanced?'advancedMode':'normalMode');byId('settingsModeHelp').textContent=tx(advanced?'advancedModeHelp':'normalModeHelp');byId('settingsSearch').value='';activeSettingsCategory='access';lastSettingsCategory='access';if(persist){try{localStorage.setItem('archive-relay-settings-mode',settingsMode);}catch(_){}}if(settingsModel)renderSettings();}
    function configuredText(value){const text=String(value??'').trim();return text!==''&&!text.startsWith('REPLACE_WITH_');}
    function validSourceEntries(){return ['users','servers','categories','channels'].flatMap((key)=>(settingsModel[key]||[]).map((item)=>({key,item}))).filter(({key,item})=>configuredText(item?.[{users:'user',servers:'server',categories:'category',channels:'channel'}[key]])&&item?.enabled!==false&&item?.save!==false);}
    function validateDraft(){if(!settingsModel)return [];const issues=[];const error=(message)=>issues.push({level:'error',message}),warning=(message)=>issues.push({level:'warning',message});if(Number(settingsModel.downloadWorkers)<1)error(bi('Worker unduhan minimal 1.','Download workers must be at least 1.'));if(Number(settingsModel.downloadQueueSize)<Number(settingsModel.downloadWorkers))error(bi('Kapasitas antrean harus sama atau lebih besar dari jumlah worker.','Queue capacity must be equal to or greater than the worker count.'));if(Number(settingsModel.downloadTimeout)<10||Number(settingsModel.discordTimeout)<10)error(bi('Timeout Discord dan unduhan minimal 10 detik.','Discord and download timeouts must be at least 10 seconds.'));if(Number(settingsModel.historyRequestCount)<1||Number(settingsModel.historyRequestCount)>100)error(bi('Pesan per request history harus antara 1 dan 100.','History messages per request must be between 1 and 100.'));if(!/^(localhost|127(?:\.\d{1,3}){3}|\[::1\]):\d+$/.test(String(settingsModel.guiAddress||'')))error(bi('Alamat GUI harus localhost/loopback dalam format host:port.','GUI address must be localhost/loopback in host:port format.'));const hasSource=Boolean(settingsModel.all&&configuredText(settingsModel.all.destination))||validSourceEntries().length>0;if(!hasSource)warning(bi('Belum ada sumber arsip valid. Nilai REPLACE_WITH_... hanya contoh dan tidak dihitung.','No valid archive source is configured. REPLACE_WITH_... values are placeholders and do not count.'));if(validSourceEntries().some(({item})=>!configuredText(item.destination)))warning(bi('Ada sumber yang belum memiliki folder tujuan.','One or more sources do not have a destination folder.'));if(settingsModel.save&&!['saveImages','saveVideos','saveAudioFiles','saveTextFiles','saveOtherFiles'].some((key)=>settingsModel[key]))warning(bi('Penyimpanan aktif tetapi semua jenis file sedang dimatikan.','Saving is enabled, but every file type is disabled.'));return issues;}
    function updateValidation(){const box=byId('settingsValidation'),issues=validateDraft();if(!issues.length){box.className='settings-validation';box.textContent='';return issues;}const errors=issues.filter((item)=>item.level==='error').length;box.className='settings-validation show'+(errors?' error':'');box.textContent=(errors?bi(errors+' masalah harus diperbaiki. ',errors+' issue(s) must be fixed. '):'')+issues.slice(0,3).map((item)=>item.message).join(' ')+(issues.length>3?bi(' +'+(issues.length-3)+' catatan lain.',' +'+(issues.length-3)+' more note(s).'):'');return issues;}
    function updateReadiness(){if(!settingsModel)return;const hasCredential=Boolean(settingsSecretFields['credentials.token']||configuredText(settingsModel.credentials?.token));const entries=validSourceEntries(),globalDestination=settingsModel.all&&configuredText(settingsModel.all.destination),sources=entries.length>0||Boolean(globalDestination),hasDestination=Boolean(globalDestination)||entries.some(({item})=>configuredText(item.destination)),healthy=Number(settingsModel.downloadWorkers)>0&&Number(settingsModel.downloadQueueSize)>=Number(settingsModel.downloadWorkers);const checks=[hasCredential,sources,hasDestination,healthy],complete=checks.filter(Boolean).length,percent=Math.round(complete/checks.length*100);const ring=byId('settingsProgress');ring.textContent=percent+'%';ring.style.setProperty('--progress',(percent*3.6)+'deg');byId('settingsChecklist').textContent=bi(complete+' dari '+checks.length+' komponen inti siap · token, sumber, tujuan, mesin unduhan',complete+' of '+checks.length+' core components ready · token, sources, destinations, download engine');}
    function updateSettingsState(saved=false){if(!settingsModel)return;const dirty=JSON.stringify(settingsModel)!==JSON.stringify(settingsOriginal),badge=byId('settingsDirty');badge.textContent=saved?bi('Tersimpan','Saved'):(dirty?bi('Ada perubahan','Unsaved changes'):bi('Tidak ada perubahan','No changes'));badge.className='settings-dirty'+(saved?' saved':(dirty?' changed':''));byId('settingsSave').disabled=!dirty;byId('settingsDiscard').disabled=!dirty;updateValidation();updateReadiness();}
    function filterSettings(){const query=byId('settingsSearch').value.trim();activeSettingsCategory=query?'search':lastSettingsCategory;renderSettings();}
    function settingsFileLabel(){return tx(lastStatus?.runtime?.state==='waiting'?'settingsFileStart':'settingsFileRestart',{file:settingsFilePath});}
    async function loadSettings(force=false){if(!force&&settingsModel&&JSON.stringify(settingsModel)!==JSON.stringify(settingsOriginal)&&!confirm(bi('Buang perubahan yang belum disimpan dan muat ulang file?','Discard unsaved changes and reload the file?')))return;const button=byId('settingsReload');button.disabled=true;try{const data=await api('/api/config',{headers:{'X-DDG-GUI-Token':token}});settingsModel=data.config;settingsOriginal=cloneValue(data.config);settingsTemplates=data.templates;settingsSecretFields=data.secretFields||{};settingsFilePath=data.file||'settings file';byId('settingsFile').textContent=settingsFileLabel();renderSettings();updateSettingsState();}catch(error){toast(error.message,true);}finally{button.disabled=false;}}
    async function saveSettings(){const button=byId('settingsSave');button.disabled=true;try{commitJSONSettings();const issues=updateValidation();if(issues.some((item)=>item.level==='error'))throw new Error(bi('Perbaiki masalah konfigurasi sebelum menyimpan.','Fix configuration issues before saving.'));const data=await api('/api/config',{method:'POST',headers:{'Content-Type':'application/json','X-DDG-GUI-Token':token},body:JSON.stringify({config:settingsModel})});toast(data.backup?bi('Tersimpan · backup dibuat','Saved · backup created'):bi('Pengaturan tersimpan','Settings saved'));await loadSettings(true);updateSettingsState(true);await refresh();}catch(error){toast(error.message,true);updateSettingsState();}}
    function discardSettings(){if(!settingsModel||JSON.stringify(settingsModel)===JSON.stringify(settingsOriginal))return;if(!confirm(bi('Batalkan semua perubahan yang belum disimpan?','Discard all unsaved changes?')))return;settingsModel=cloneValue(settingsOriginal);renderSettings();updateSettingsState();toast(bi('Perubahan dibatalkan','Changes discarded'));}
    function exportSettings(){if(!settingsModel)return;try{commitJSONSettings();const blob=new Blob([JSON.stringify(settingsModel,null,2)],{type:'application/json'}),url=URL.createObjectURL(blob),link=document.createElement('a');link.href=url;link.download='discord-downloader-settings-draft.json';document.body.appendChild(link);link.click();link.remove();URL.revokeObjectURL(url);toast(bi('Draft konfigurasi diekspor tanpa membuka rahasia tersimpan','Configuration draft exported without exposing stored secrets'));}catch(error){toast(error.message,true);}}
    function mergeSettings(base,incoming){if(!incoming||typeof incoming!=='object'||Array.isArray(incoming))return cloneValue(incoming);const result=cloneValue(base)||{};Object.entries(incoming).forEach(([key,value])=>{result[key]=value&&typeof value==='object'&&!Array.isArray(value)?mergeSettings(result[key],value):cloneValue(value);});return result;}
    function importSettings(file){if(!file)return;const reader=new FileReader();reader.onload=()=>{try{const imported=JSON.parse(reader.result);if(!imported||typeof imported!=='object'||Array.isArray(imported))throw new Error(bi('Root JSON harus berupa object.','The JSON root must be an object.'));settingsModel=mergeSettings(settingsModel,imported);renderSettings();updateSettingsState();toast(bi('Draft konfigurasi berhasil diimpor','Configuration draft imported'));}catch(error){toast(bi('Impor gagal: ','Import failed: ')+error.message,true);}finally{byId('settingsImportFile').value='';}};reader.readAsText(file);}
    function toast(message,error){const el=byId('toast');el.textContent=message;el.className='toast show'+(error?' error':'');clearTimeout(window.toastTimer);window.toastTimer=setTimeout(()=>el.className='toast',3200);}
    async function api(path,options){const response=await fetch(path,options);let data={};try{data=await response.json();}catch(_){data={error:'Invalid server response'};}if(!response.ok)throw new Error(data.error||('HTTP '+response.status));return data;}
    function jobKind(job){if(/ERROR/i.test(job.status))return 'failed';if(/Waiting|Downloading|Abort Requested/i.test(job.status))return 'active';return 'completed';}
    function renderJobs(){
      const query=byId('jobsSearch').value.trim().toLowerCase();
      const visible=lastJobs.filter((job)=>(jobFilter==='all'||jobKind(job)===jobFilter)&&(!query||(job.channelId+' '+job.status+' '+job.origin).toLowerCase().includes(query)));
      byId('jobCount').textContent=visible.length;byId('emptyJobs').style.display=visible.length?'none':'block';
      byId('jobsBody').innerHTML=visible.map((job)=>{const active=jobKind(job)==='active',failed=jobKind(job)==='failed';return '<tr><td>'+escapeHtml(job.channelId)+'</td><td><span class="status '+(failed?'failed':'')+'">'+escapeHtml(job.status)+'</span></td><td>'+escapeHtml(job.origin)+'</td><td>'+escapeHtml(relative(job.updated))+'</td><td>'+(active?'<button class="danger cancel-job" data-channel="'+escapeHtml(job.channelId)+'">'+escapeHtml(tx('cancel'))+'</button>':'—')+'</td></tr>';}).join('');
      document.querySelectorAll('.cancel-job').forEach((button)=>button.addEventListener('click',()=>cancelJob(button.dataset.channel)));
    }
    function updatePulse(downloads){
      const pulse=byId('activityPulse');if(!pulse.children.length){for(let i=0;i<28;i++)pulse.appendChild(document.createElement('span'));}
      const load=Math.min(1,(Number(downloads.inFlight)+Number(downloads.queued))/Math.max(1,Number(downloads.workers)*3));
      Array.from(pulse.children).forEach((bar,index)=>{const wave=(Math.sin(index*.72+Number(downloads.total)%11)+1)/2;bar.style.height=(12+wave*(22+load*56))+'%';});
    }
    function renderRuntimeControl(status){
      const state=status.runtime?.state||'initializing',labels={waiting:'runtimeWaiting',starting:'runtimeStarting',running:'runtimeRunning',stopping:'runtimeStopping',initializing:'runtimeInitializing'};
      const stateElement=byId('runtimeState'),startButton=byId('runtimeStartButton'),running=state==='running';stateElement.textContent=tx(labels[state]||'runtimeInitializing');stateElement.classList.toggle('running',running);startButton.disabled=state!=='waiting';startButton.textContent=tx(state==='waiting'?'startRuntime':(state==='starting'?'runtimeStarting':(running?'runtimeRunning':(state==='stopping'?'runtimeStopping':'runtimeInitializing'))));
      return state;
    }
    function render(status){
      lastStatus=status;lastJobs=status.history.jobs||[];
      const runtimeState=renderRuntimeControl(status);
      if(settingsModel)byId('settingsFile').textContent=settingsFileLabel();
      const setup=byId('setupBanner');if(status.startupIssue){setup.textContent=bi('Perlu perhatian: ','Attention required: ')+status.startupIssue;setup.className='setup show';}else{setup.textContent='';setup.className='setup';}
      byId('readyDot').className='dot'+(status.bot.ready?' ok':'');byId('readyText').textContent=runtimeState==='waiting'?tx('runtimeWaiting'):(status.bot.ready?(status.bot.username+' '+tx('online')):tx('discordNotReady'));
      byId('totalDownloads').textContent=Number(status.downloads.total).toLocaleString(currentLanguage==='en'?'en-US':'id-ID');byId('lastDownload').textContent=status.downloads.lastDownload?tx('lastDownloaded',{time:relative(status.downloads.lastDownload)}):tx('noRecentDownload');
      byId('queueValue').textContent=tx('activeTransfers',{n:status.downloads.inFlight});byId('workerValue').textContent=tx('workerSummary',{queued:status.downloads.queued,workers:status.downloads.workers});
      byId('historyValue').textContent=tx('historyRunning',{n:status.history.running});byId('historyHint').textContent=tx('historySummary',{waiting:status.history.waiting,failed:status.history.failed});
      byId('guildValue').textContent=Number(status.bot.guildCount).toLocaleString(currentLanguage==='en'?'en-US':'id-ID');byId('uptimeValue').textContent='Uptime '+duration(status.uptimeSeconds);byId('version').textContent='v'+status.version;
      const config=currentLanguage==='en'?[['Settings file',status.config.file],['Channels',status.config.boundChannels],['Categories',status.config.boundCategories],['Servers',status.config.boundServers],['Users',status.config.boundUsers],['Timeout',status.config.downloadTimeout+' sec'],['Retries',status.config.downloadRetryMax],['File limit',Math.round(status.config.maxFileSize/1048576)+' MB'],['History slots',status.config.historyMaxJobs],['Settings watch',status.config.watchSettings?'On':'Off']]:[['File pengaturan',status.config.file],['Channel',status.config.boundChannels],['Kategori',status.config.boundCategories],['Server',status.config.boundServers],['Pengguna',status.config.boundUsers],['Timeout',status.config.downloadTimeout+' dtk'],['Percobaan ulang',status.config.downloadRetryMax],['Batas file',Math.round(status.config.maxFileSize/1048576)+' MB'],['Slot riwayat',status.config.historyMaxJobs],['Pantau file',status.config.watchSettings?'Aktif':'Mati']];
    byId('configGrid').innerHTML=config.map((x)=>'<div>'+escapeHtml(x[0])+'</div><div>'+escapeHtml(x[1])+'</div>').join('');renderJobs();updatePulse(status.downloads);
      const notes=[];if(runtimeState==='waiting')notes.push(tx('waitingStartHelp'));else if(runtimeState==='starting')notes.push(tx('startingHelp'));else{if(!status.bot.ready)notes.push(bi('Koneksi Discord belum siap.','Discord connection is not ready.'));if(status.downloads.queueStopped)notes.push(bi('Antrean transfer sedang berhenti.','The transfer queue is stopped.'));else if(status.downloads.queued>status.downloads.workers*4)notes.push(bi('Beban antrean transfer sedang tinggi.','The transfer queue is under heavy load.'));if(status.history.failed)notes.push(bi(status.history.failed+' job riwayat perlu diperiksa.',status.history.failed+' history job(s) need attention.'));}if(!notes.length)notes.push(bi('Semua sistem yang dipantau dalam kondisi baik.','All monitored systems are healthy.'));byId('healthNotes').textContent=notes.join(' ');
    }
    async function refresh(){const button=byId('refreshButton');button.disabled=true;try{render(await api('/api/status'));}catch(error){toast(error.message,true);}finally{button.disabled=false;}}
    function consoleLineKind(line){if(/\b(FATAL|PANIC|ERROR|FAILED|FAILURE)\b|unable to|cannot |gagal/i.test(line))return 'error';if(/\b(WARN|WARNING|RETRY|INVALID|MISSING)\b|belum siap|not ready|rate.?limit/i.test(line))return 'warning';if(/\b(DEBUG|TRACE|VERBOSE)\b|Parsed into JSON/i.test(line))return 'debug';if(/\b(READY|ONLINE|CONNECTED|COMPLETED|DOWNLOADED|SUCCESS)\b|Welcome to|available at/i.test(line))return 'success';if(/\bDiscord\b|discord\.go/i.test(line))return 'discord';if(/^\s*[{}\[\],"]|^\s+"[^"]+"\s*:/.test(line))return 'json';return 'info';}
    function consoleLineHTML(line){const match=String(line).match(/^(\d{4}\/\d{2}\/\d{2}\s+\d{2}:\d{2}:\d{2})\s+(\S+?:\d+:)?\s*(.*)$/),kind=consoleLineKind(line);if(!match)return '<span class="console-line '+kind+'">'+escapeHtml(line)+'</span>';const rest=escapeHtml(match[3]).replace(/(\[[^\]]+\])/g,'<span class="log-tag">$1</span>');return '<span class="console-line '+kind+'"><span class="log-time">'+escapeHtml(match[1])+'</span> <span class="log-source">'+escapeHtml(match[2]||'')+'</span> '+rest+'</span>';}
    function renderConsole(){const output=byId('consoleOutput');output.dataset.empty=tx('waitingLogs');output.innerHTML=consoleLines.map(consoleLineHTML).join('');byId('consoleCount').textContent=consoleLines.length;if(consoleAutoScroll)output.scrollTop=output.scrollHeight;}
    async function refreshConsole(){if(consoleLoading)return;consoleLoading=true;try{const data=await api('/api/logs?since='+consoleCursor,{headers:{'X-DDG-GUI-Token':token}});consoleCursor=Number(data.next)||consoleCursor;byId('consoleStatus').dataset.i18n='consoleLive';byId('consoleStatus').textContent=tx('consoleLive');if(Array.isArray(data.entries)&&data.entries.length){consoleLines.push(...data.entries.map((entry)=>entry.text));if(consoleLines.length>1000)consoleLines=consoleLines.slice(-1000);renderConsole();}}catch(error){byId('consoleStatus').removeAttribute('data-i18n');byId('consoleStatus').textContent=bi('LOG TERPUTUS','LOG OFFLINE');}finally{consoleLoading=false;}}
    async function copyConsole(){const text=consoleLines.join('\n');if(!text){toast(bi('Belum ada log untuk disalin','No logs to copy yet'),true);return;}try{await navigator.clipboard.writeText(text);toast(bi('Log aplikasi disalin','Application logs copied'));}catch(_){const area=document.createElement('textarea');area.value=text;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();const copied=document.execCommand('copy');area.remove();toast(copied?bi('Log aplikasi disalin','Application logs copied'):bi('Gagal menyalin log','Failed to copy logs'),!copied);}}
    async function cancelJob(channelId){try{await api('/api/history/cancel',{method:'POST',headers:{'Content-Type':'application/json','X-DDG-GUI-Token':token},body:JSON.stringify({channelId:channelId})});toast(bi('Permintaan pembatalan dikirim','Cancellation requested'));refresh();}catch(error){toast(error.message,true);}}
    async function startRuntime(){const button=byId('runtimeStartButton');button.disabled=true;try{await api('/api/runtime/start',{method:'POST',headers:{'X-DDG-GUI-Token':token}});toast(bi('Runtime sedang dimulai','Runtime is starting'));await refresh();}catch(error){toast(error.message,true);await refresh();}}
    async function copySnapshot(){if(!lastStatus){toast(bi('Status belum tersedia','Status is not available yet'),true);return;}const text=JSON.stringify(lastStatus,null,2);try{if(navigator.clipboard?.writeText)await navigator.clipboard.writeText(text);else throw new Error('clipboard unavailable');toast(bi('Status runtime disalin','Runtime status copied'));}catch(_){const area=document.createElement('textarea');area.value=text;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();const copied=document.execCommand('copy');area.remove();toast(copied?bi('Status runtime disalin','Runtime status copied'):bi('Gagal menyalin status','Failed to copy status'),!copied);}}
    document.querySelectorAll('[data-job-filter]').forEach((button)=>button.addEventListener('click',()=>{jobFilter=button.dataset.jobFilter;document.querySelectorAll('[data-job-filter]').forEach((item)=>{const selected=item===button;item.classList.toggle('active',selected);item.setAttribute('aria-pressed',selected);});renderJobs();}));
    byId('jobsSearch').addEventListener('input',renderJobs);byId('settingsSearch').addEventListener('input',filterSettings);byId('settingsReload').addEventListener('click',()=>loadSettings());byId('settingsSave').addEventListener('click',saveSettings);byId('settingsDiscard').addEventListener('click',discardSettings);byId('settingsExport').addEventListener('click',exportSettings);byId('settingsImport').addEventListener('click',()=>byId('settingsImportFile').click());byId('settingsImportFile').addEventListener('change',(event)=>importSettings(event.target.files[0]));
    byId('settingsModeNormal').addEventListener('click',()=>setSettingsMode('normal'));byId('settingsModeAdvanced').addEventListener('click',()=>setSettingsMode('advanced'));
    byId('consoleFollow').addEventListener('click',()=>{consoleAutoScroll=!consoleAutoScroll;const button=byId('consoleFollow');button.dataset.i18n=consoleAutoScroll?'autoScrollOn':'autoScrollOff';button.textContent=tx(button.dataset.i18n);button.classList.toggle('active',consoleAutoScroll);button.setAttribute('aria-pressed',consoleAutoScroll);if(consoleAutoScroll)renderConsole();});
    byId('consoleCopy').addEventListener('click',copyConsole);byId('consoleClear').addEventListener('click',()=>{consoleLines=[];renderConsole();});
    byId('liveToggle').addEventListener('click',()=>{autoRefresh=!autoRefresh;const button=byId('liveToggle');button.textContent=tx(autoRefresh?'liveOn':'livePaused');button.classList.toggle('active',autoRefresh);button.setAttribute('aria-pressed',autoRefresh);if(autoRefresh)refresh();});
    byId('languageSelect').addEventListener('change',(event)=>{applyLanguage(event.target.value);byId('liveToggle').textContent=tx(autoRefresh?'liveOn':'livePaused');renderConsole();if(lastStatus)render(lastStatus);if(settingsModel){byId('settingsFile').textContent=settingsFileLabel();renderSettings();updateSettingsState();}});byId('themeSelect').addEventListener('change',(event)=>applyTheme(event.target.value));byId('runtimeStartButton').addEventListener('click',startRuntime);byId('copySnapshot').addEventListener('click',copySnapshot);byId('refreshButton').addEventListener('click',refresh);setSettingsMode(settingsMode,false);renderConsole();refreshConsole();refresh();loadSettings();setInterval(refreshConsole,1200);setInterval(()=>{if(autoRefresh)refresh();},2500);
  </script>
</body>
</html>`
