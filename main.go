package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Davincible/goinsta/v3"
	"github.com/HouzuoGuo/tiedot/db"
	"github.com/Necroforger/dgrouter/exrouter"
	"github.com/bwmarrin/discordgo"
	"github.com/fatih/color"
	"github.com/fsnotify/fsnotify"
	twitterscraper "github.com/imperatrona/twitter-scraper"
	"github.com/rivo/duplo"
)

var (
	// General
	loop                      chan os.Signal
	mainWg                    sync.WaitGroup
	historyManagerWG          sync.WaitGroup
	historyWorkersWG          sync.WaitGroup
	historyManagerStop        chan struct{}
	historyManagerStopOnce    sync.Once
	runtimeBackgroundStop     chan struct{}
	runtimeBackgroundStopOnce sync.Once
	runtimeBackgroundWG       sync.WaitGroup
	startTime                 time.Time
	ddgUpdateAvailable        bool = false
	autoHistoryInitiated      atomic.Bool

	// Downloads
	timeLastUpdated      time.Time
	timeLastDownload     time.Time
	timeLastMessage      time.Time
	runtimeTelemetryMu   sync.RWMutex
	cachedDownloadID     int
	configReloadLastTime time.Time

	// Discord
	discordSessionMu sync.RWMutex
	bot              *discordgo.Session
	botUser          *discordgo.User
	botCommands      *exrouter.Route
	selfbot          bool = false
	botReady         atomic.Bool

	// Storage
	myDB         *db.DB
	duploCatalog *duplo.Store

	// APIs
	twitterConnected   bool = false
	twitterScraper     *twitterscraper.Scraper
	instagramConnected bool = false
	instagramClient    *goinsta.Instagram
)

func setDiscordSession(session *discordgo.Session) {
	discordSessionMu.Lock()
	bot = session
	discordSessionMu.Unlock()
}

func currentDiscordSession() *discordgo.Session {
	discordSessionMu.RLock()
	session := bot
	discordSessionMu.RUnlock()
	return session
}

func setDiscordUser(user *discordgo.User) {
	discordSessionMu.Lock()
	botUser = user
	discordSessionMu.Unlock()
}

func currentDiscordUser() *discordgo.User {
	discordSessionMu.RLock()
	user := botUser
	discordSessionMu.RUnlock()
	return user
}

func discordUserID() string {
	user := currentDiscordUser()
	if user == nil {
		return ""
	}
	return user.ID
}

func discordGuildCount() int {
	session := currentDiscordSession()
	if session == nil || session.State == nil {
		return 0
	}
	session.State.RLock()
	count := len(session.State.Guilds)
	session.State.RUnlock()
	return count
}

// discordGuildsSnapshot copies the guild and channel fields used by scans so
// gateway updates cannot mutate their slices while another goroutine iterates.
func discordGuildsSnapshot() []*discordgo.Guild {
	session := currentDiscordSession()
	if session == nil || session.State == nil {
		return nil
	}
	session.State.RLock()
	guilds := make([]*discordgo.Guild, 0, len(session.State.Guilds))
	for _, guild := range session.State.Guilds {
		if guild == nil {
			continue
		}
		guildCopy := *guild
		guildCopy.Channels = make([]*discordgo.Channel, 0, len(guild.Channels))
		for _, channel := range guild.Channels {
			if channel == nil {
				continue
			}
			channelCopy := *channel
			guildCopy.Channels = append(guildCopy.Channels, &channelCopy)
		}
		guilds = append(guilds, &guildCopy)
	}
	session.State.RUnlock()
	return guilds
}

func setLastUpdated(value time.Time) {
	runtimeTelemetryMu.Lock()
	timeLastUpdated = value
	runtimeTelemetryMu.Unlock()
}

func setLastDownload(value time.Time) {
	runtimeTelemetryMu.Lock()
	timeLastDownload = value
	runtimeTelemetryMu.Unlock()
}

func setLastMessage(value time.Time) {
	runtimeTelemetryMu.Lock()
	timeLastMessage = value
	runtimeTelemetryMu.Unlock()
}

func runtimeTelemetrySnapshot() (updated, download, message time.Time) {
	runtimeTelemetryMu.RLock()
	updated, download, message = timeLastUpdated, timeLastDownload, timeLastMessage
	runtimeTelemetryMu.RUnlock()
	return
}

func runtimeIsStopping() bool {
	select {
	case <-runtimeBackgroundStop:
		return true
	default:
		return false
	}
}

func waitForRuntimeStop(duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-runtimeBackgroundStop:
		return true
	case <-timer.C:
		return false
	}
}

func requestShutdown() bool {
	if loop == nil {
		return false
	}
	select {
	case loop <- os.Interrupt:
		return true
	default:
		return false
	}
}

func versions(multiline bool) string {
	if multiline {
		return fmt.Sprintf("%s/%s / %s\ndiscordgo v%s (API v%s)",
			runtime.GOOS, runtime.GOARCH, runtime.Version(), discordgo.VERSION, discordgo.APIVersion)
	} else {
		return fmt.Sprintf("%s/%s / %s / discordgo v%s (API v%s)",
			runtime.GOOS, runtime.GOARCH, runtime.Version(), discordgo.VERSION, discordgo.APIVersion)
	}
}

// waitForRuntimeStart keeps the local control surface available without opening
// the database, starting workers, or connecting to Discord. A manual start
// reloads the settings file so edits saved from the waiting dashboard are used
// by the runtime that is about to start.
func waitForRuntimeStart(configErr error) bool {
	if guiServer == nil {
		setGUIRuntimeState("starting")
		return configErr == nil
	}
	if configErr == nil && shouldAutoStartGUIRuntime() {
		setGUIRuntimeState("starting")
		return true
	}

	setGUIRuntimeState("waiting")
	log.Println(lg("GUI", "Runtime", color.HiCyanString, "Runtime is waiting for the Start button"))
	signal.Notify(loop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, os.Interrupt)
	defer signal.Stop(loop)
	for {
		select {
		case <-loop:
			setGUIRuntimeState("stopping")
			return false
		case <-guiRuntimeStart:
			setGUIRuntimeState("starting")
			log.Println(lg("GUI", "Runtime", color.HiGreenString, "Start requested; reloading settings before startup"))
			if err := loadConfig(); err != nil {
				setGUIStartupIssue(err.Error())
				setGUIRuntimeState("waiting")
				log.Println(lg("GUI", "Runtime", color.HiRedString, "Unable to start: %s", err))
				continue
			}
			if err := validateRuntimeCredentials(config); err != nil {
				setGUIStartupIssue(err.Error())
				setGUIRuntimeState("waiting")
				log.Println(lg("GUI", "Runtime", color.HiRedString, "Unable to start: %s", err))
				continue
			}
			setGUIStartupIssue("")
			return true
		}
	}
}

func init() {
	configureDiscordAPI()

	//#region Initialize Logging
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	initGUILogCapture(color.Output)
	log.Println(color.HiGreenString(wrapHyphensW(fmt.Sprintf("Welcome to %s v%s", projectName, projectVersion))))
	//#endregion

	//#region Initialize Variables
	loop = make(chan os.Signal, 1)
	historyManagerStop = make(chan struct{})
	runtimeBackgroundStop = make(chan struct{})

	startTime = time.Now()

	historyJobs = newHistoryJobStore()

	parseRuntimeArgs(os.Args[1:])
	//#endregion

}

func main() {

	//#region <<< CRITICAL INIT >>>

	configErr := loadConfig()
	if configErr == nil {
		configErr = validateRuntimeCredentials(config)
	}
	if configErr != nil {
		setGUIStartupIssue(configErr.Error())
	}
	if shouldStartGUI() {
		if err := startGUI(); err != nil {
			log.Println(lg("GUI", "Startup", color.HiRedString, "Unable to start GUI: %s", err))
		}
	}
	if configErr != nil && guiServer != nil {
		log.Println(lg("GUI", "Setup", color.HiYellowString, "Dashboard is staying open for setup: %s", configErr))
	}
	if !waitForRuntimeStart(configErr) {
		hadGUI := guiServer != nil
		shutdownGUI()
		if hadGUI {
			return
		}
		properExit()
	}
	openDatabase()
	if !databaseReady() {
		log.Println(lg("Database", "Startup", color.HiRedString, "Download database is unavailable; refusing to start an unsafe downloader session"))
		properExit()
	}
	startDownloadManager(config.DownloadWorkers, config.DownloadQueueSize)

	// Run the network-dependent update check only after the local GUI is serving.
	// A slow or unavailable network must never prevent the dashboard from opening.
	if config.GithubUpdateChecking {
		ddgUpdateAvailable = !isLatestGithubRelease()
	}

	//#endregion

	// Output Flag Warnings
	if config.Verbose {
		log.Println(lg("VERBOSE", "", color.HiBlueString, "VERBOSE OUTPUT ENABLED ... just some extra info..."))
	}
	if config.Debug {
		log.Println(lg("DEBUG", "", color.HiYellowString, "DEBUGGING OUTPUT ENABLED ... some troubleshooting data..."))
	}
	if config.DebugExtra {
		log.Println(lg("DEBUG2", "", color.YellowString, "EXTRA DEBUGGING OUTPUT ENABLED ... some in-depth troubleshooting data..."))
	}

	mainWg.Wait() // wait because credentials from config

	//#region <<< CONNECTIONS >>>

	mainWg.Add(2)

	go botLoadAPIs()

	go botLoadDiscord()

	//#endregion

	//#region Initialize Regex
	mainWg.Add(1)
	go func() {
		defer mainWg.Done()
		if err := compileRegex(); err != nil {
			log.Println(lg("Regex", "", color.HiRedString, "Error initializing:\t%s", err))
			return
		}
	}()
	//#endregion

	//#region [Loops] History Job Processing
	historyManagerWG.Add(1)
	go func() {
		defer historyManagerWG.Done()
		for {
			select {
			case <-historyManagerStop:
				return
			default:
			}
			newJobCount := 0
			// Empty Local Cache
			nhistoryJobCnt,
				nhistoryJobCntWaiting,
				nhistoryJobCntRunning,
				nhistoryJobCntAborted,
				nhistoryJobCntErrored,
				nhistoryJobCntCompleted := historyJobs.Len(), 0, 0, 0, 0, 0

			//MARKER: history jobs launch
			// do we even bother?
			if nhistoryJobCnt > 0 {
				// New Cache
				for _, pair := range historyJobs.Snapshot() {
					job := pair.Value
					if job.Status == historyStatusWaiting {
						nhistoryJobCntWaiting++
					} else if job.Status == historyStatusRunning {
						nhistoryJobCntRunning++
					} else if job.Status == historyStatusAbortRequested || job.Status == historyStatusAbortCompleted {
						nhistoryJobCntAborted++
					} else if job.Status == historyStatusErrorReadMessageHistoryPerms || job.Status == historyStatusErrorRequesting {
						nhistoryJobCntErrored++
					} else if job.Status >= historyStatusCompletedNoMoreMessages {
						nhistoryJobCntCompleted++
					}
				}

				// Should Start New Job(s)?
				if nhistoryJobCntRunning < config.HistoryMaxJobs || config.HistoryMaxJobs < 1 {
					openSlots := config.HistoryMaxJobs - nhistoryJobCntRunning
					if openSlots < 1 {
						openSlots = 1
					}
					newJobKeys := make([]string, 0, openSlots)
					// Find Jobs
					for _, pair := range historyJobs.Snapshot() {
						if newJobCount == openSlots {
							break
						}
						if pair.Value.Status == historyStatusWaiting {
							newJobKeys = append(newJobKeys, pair.Key)
							newJobCount++
						}
					}
					// Start Jobs
					if len(newJobKeys) > 0 {
						for _, key := range newJobKeys {
							job, started := historyJobs.StartWaiting(key)
							if !started {
								continue
							}
							historyWorkersWG.Add(1)
							go func(historyJob historyJob) {
								defer historyWorkersWG.Done()
								handleHistory(historyJob.TargetCommandingMessage, historyJob.TargetChannelID, historyJob.TargetBefore, historyJob.TargetSince)
							}(job)
						}
					}
				}
			}

			// Update Cache
			setHistoryJobStats(nhistoryJobCnt, nhistoryJobCntWaiting, nhistoryJobCntRunning,
				nhistoryJobCntAborted, nhistoryJobCntErrored, nhistoryJobCntCompleted)

			// Wait before checking again
			timer := time.NewTimer(safeConfiguredDuration(config.HistoryManagerRate, time.Second))
			select {
			case <-timer.C:
			case <-historyManagerStop:
				timer.Stop()
				return
			}

			// Auto Exit
			if config.AutoHistoryExit && autoHistoryInitiated.Load() &&
				historyJobs.Len() > 0 && newJobCount == 0 && historyJobCntWaiting == 0 && historyJobCntRunning == 0 {
				log.Println(lg("History", "", color.HiCyanString, "Exiting due to auto history completion..."))
				requestShutdown()
				return
			}
		}
	}()
	//#endregion

	mainWg.Wait() // Once complete, bot is functional
	setGUIRuntimeState("running")

	//#region MISC STARTUP OUTPUT - Github Update Notification, Version, Discord Invite

	log.Println(lg("Version", "", color.MagentaString, "%s", versions(false)))

	if config.GithubUpdateChecking {
		if ddgUpdateAvailable {
			log.Println(lg("Version", "UPDATE", color.HiGreenString, "***\tUPDATE AVAILABLE\t***"))
			log.Println(lg("Version", "UPDATE", color.HiGreenString, "%s", "DOWNLOAD:\n\n"+projectRepoURL+"/releases/latest\n\n"))
			log.Println(lg("Version", "UPDATE", color.HiGreenString, "%s",
				fmt.Sprintf("You are on v%s, latest is %s", projectVersion, latestGithubRelease),
			))
			log.Println(lg("Version", "UPDATE", color.GreenString, "*** See changelogs for information ***"))
			log.Println(lg("Version", "UPDATE", color.GreenString, "Check ALL changelogs since your last update!"))
			log.Println(lg("Version", "UPDATE", color.HiGreenString, "SOME SETTINGS-BREAKING CHANGES MAY HAVE OCCURED!!"))
			time.Sleep(5 * time.Second)
		} else {
			if "v"+projectVersion == latestGithubRelease {
				log.Println(lg("Version", "UPDATE", color.GreenString, "You are on the latest version, v%s", projectVersion))
			} else {
				log.Println(lg("Version", "UPDATE", color.GreenString, "No updates available, you are on v%s, latest is %s", projectVersion, latestGithubRelease))
			}
		}
	}

	log.Println(lg("Info", "", color.HiCyanString, "** NEED HELP? discord-downloader-go Discord Server: https://discord.gg/6Z6FJZVaDV **"))

	//#endregion

	//#region <<< MAIN STARTUP COMPLETE - BOT IS FUNCTIONAL >>>

	if config.Verbose {
		log.Println(lg("Verbose", "Startup", color.HiBlueString, "Startup finished, took %s...", uptime()))
	}
	log.Println(lg("Main", "", color.HiGreenString, "%s",
		wrapHyphensW(fmt.Sprintf("%s v%s is online with access to %d server%s",
			projectLabel, projectVersion, discordGuildCount(), pluralS(discordGuildCount())))))
	log.Println(lg("Main", "", color.RedString, "CTRL+C to exit..."))

	// Log Status
	go sendStatusMessage(sendStatusStartup)

	//#endregion

	//#region Autorun History
	type arh struct{ channel, before, since string }
	var autoHistoryChannels []arh
	// Compile list of channels to autorun history
	for _, channel := range getAllRegisteredChannels() {
		sourceConfig := getSource(&discordgo.Message{ChannelID: channel.ChannelID})
		if sourceConfig.AutoHistory != nil {
			if *sourceConfig.AutoHistory {
				var autoHistoryChannel arh
				autoHistoryChannel.channel = channel.ChannelID
				autoHistoryChannel.before = *sourceConfig.AutoHistoryBefore
				autoHistoryChannel.since = *sourceConfig.AutoHistorySince
				autoHistoryChannels = append(autoHistoryChannels, autoHistoryChannel)
			}
			continue
		}
	}
	// Process auto history
	for _, ah := range autoHistoryChannels {
		//MARKER: history jobs queued from auto
		now := time.Now()
		historyJobs.Queue(ah.channel, historyJob{
			Status:          historyStatusWaiting,
			OriginChannel:   "AUTORUN",
			OriginUser:      "AUTORUN",
			TargetChannelID: ah.channel,
			TargetBefore:    ah.before,
			TargetSince:     ah.since,
			Updated:         now,
			Added:           now,
		})
		//TODO: signals for this and typical history cmd??
	}
	autoHistoryInitiated.Store(true)
	if len(autoHistoryChannels) > 0 {
		log.Println(lg("History", "Autorun", color.HiYellowString,
			"History Autoruns completed (for %d channel%s)",
			len(autoHistoryChannels), pluralS(len(autoHistoryChannels))))
		log.Println(lg("History", "Autorun", color.CyanString,
			"Waiting for something else to do..."))
	}
	//#endregion

	//#region [Loops] Tickers
	tickerCheckup := time.NewTicker(safeConfiguredDuration(config.CheckupRate, time.Minute))
	tickerPresence := time.NewTicker(safeConfiguredDuration(config.PresenceRefreshRate, time.Minute))
	tickerConnection := time.NewTicker(safeConfiguredDuration(config.ConnectionCheckRate, time.Minute))
	runtimeBackgroundWG.Add(1)
	go func() {
		defer runtimeBackgroundWG.Done()
		defer tickerCheckup.Stop()
		defer tickerPresence.Stop()
		defer tickerConnection.Stop()
		for {
			select {
			case <-runtimeBackgroundStop:
				return

			case <-tickerCheckup.C:
				if config.Debug {
					session := currentDiscordSession()
					if session == nil {
						continue
					}
					//MARKER: history jobs polled for waiting count in checkup
					historyJobsWaiting := 0
					if historyJobs.Len() > 0 {
						for _, jobPair := range historyJobs.Snapshot() {
							job := jobPair.Value
							if job.Status == historyStatusWaiting {
								historyJobsWaiting++
							}
						}
					}
					_, lastDownload, lastMessage := runtimeTelemetrySnapshot()
					str := fmt.Sprintf("... %dms latency,\t\tlast discord heartbeat %s ago,\t\t%s uptime",
						session.HeartbeatLatency().Milliseconds(),
						timeSinceShort(session.LastHeartbeatSent),
						timeSinceShort(startTime))
					if !lastMessage.IsZero() {
						str += fmt.Sprintf(",\tlast message %s ago",
							timeSinceShort(lastMessage))
					}
					if !lastDownload.IsZero() {
						str += fmt.Sprintf(",\tlast download %s ago",
							timeSinceShort(lastDownload))
					}
					if historyJobsWaiting > 0 {
						str += fmt.Sprintf(",\t%d history jobs waiting", historyJobsWaiting)
					}
					log.Println(lg("Checkup", "", color.YellowString, "%s", str))
				}

			case <-tickerPresence.C:
				// If bot experiences connection interruption the status will go blank until updated by message, this fixes that
				go updateDiscordPresence()

			case <-tickerConnection.C:
				if config.ConnectionCheck {
					session := currentDiscordSession()
					if session == nil {
						continue
					}
					doReconnect := func(session *discordgo.Session) {
						botReady.Store(false)
						log.Println(lg("Discord", "", color.YellowString, "Closing Discord connections..."))
						if session.Client != nil {
							session.Client.CloseIdleConnections()
						}
						_ = session.CloseWithCode(1001)
						log.Println(lg("Discord", "", color.RedString, "Discord connections closed!"))
						if waitForRuntimeStop(15 * time.Second) {
							return
						}
						if config.ExitOnBadConnection {
							requestShutdown()
							return
						}
						log.Println(lg("Discord", "", color.GreenString, "Logging in..."))
						var reconnectErr error
						for attempt := 1; attempt <= 5; attempt++ {
							reconnectErr = session.Open()
							if reconnectErr == nil || reconnectErr == discordgo.ErrWSAlreadyOpen {
								reconnectErr = nil
								break
							}
							log.Println(lg("Discord", "Reconnect", color.HiYellowString,
								"Reconnect attempt %d failed: %s", attempt, reconnectErr))
							if attempt < 5 && waitForRuntimeStop(3*time.Second) {
								return
							}
						}
						if reconnectErr != nil {
							setGUIStartupIssue(fmt.Sprintf("Discord reconnect failed: %s", reconnectErr))
							log.Println(lg("Discord", "Reconnect", color.HiRedString,
								"Discord reconnect failed after 5 attempts: %s", reconnectErr))
							return
						}
						setGUIStartupIssue("")
						botReady.Store(true)
						setLastUpdated(time.Now())
						go updateDiscordPresence()
						log.Println(lg("Discord", "", color.HiGreenString,
							"Reconnected! The bot should resume working."))
						sendStatusMessage(sendStatusReconnect)
					}
					gate, err := session.Gateway()
					if err != nil || gate == "" {
						log.Println(lg("Discord", "", color.HiYellowString,
							"Bot encountered a gateway error: GATEWAY: %s,\tERR: %s", gate, err))
						doReconnect(session)
					} else if time.Since(session.LastHeartbeatAck).Seconds() > 4*60 {
						log.Println(lg("Discord", "", color.HiYellowString,
							"Bot has not received a heartbeat from Discord in 4 minutes..."))
						doReconnect(session)
					}
				}
			}
		}
	}()
	//#endregion

	//#region [Loop] Settings Watcher
	if config.WatchSettings {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			log.Println(lg("Settings", "Watcher", color.HiRedString, "Error creating NewWatcher:\t%s", err))
		} else {
			defer watcher.Close()
			watchTarget, pathErr := filepath.Abs(configFile)
			if pathErr != nil {
				watchTarget = filepath.Clean(configFile)
			}
			if err = watcher.Add(filepath.Dir(watchTarget)); err != nil {
				log.Println(lg("Settings", "Watcher", color.HiRedString, "Error adding watcher for settings:\t%s", err))
			}
			go func(target string) {
				for {
					select {
					case event, ok := <-watcher.Events:
						if !ok {
							return
						}
						if !strings.EqualFold(filepath.Clean(event.Name), filepath.Clean(target)) {
							continue
						}
						if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 &&
							time.Since(configReloadLastTime) > 500*time.Millisecond {
							log.Println(lg("Settings", "Watcher", color.HiYellowString,
								"Detected changes in \"%s\"; restart required to apply them safely.", configFile))
							setGUIStartupIssue("settings changed; restart the application to apply every option")
							configReloadLastTime = time.Now()
						}
					case err, ok := <-watcher.Errors:
						if !ok {
							return
						}
						log.Println(color.HiRedString("[Watchers] Error:\t%s", err))
					}
				}
			}(watchTarget)
		}
	}
	//#endregion

	//#region Database Backup

	if config.BackupDatabaseOnStart {
		if err := backupDatabase(); err != nil {
			log.Println(lg("Database", "Backup", color.HiRedString, "Error backing up database:\t%s", err))
		}
	}

	//#endregion

	//#region Cache Constants

	runtimeBackgroundWG.Add(1)
	go func() {
		defer runtimeBackgroundWG.Done()
		constants := make(map[string]string)
		//--- Compile constants
		for _, server := range discordGuildsSnapshot() {
			if runtimeIsStopping() {
				return
			}
			serverKey := fmt.Sprintf("SERVER_%s", stripSymbols(server.Name))
			serverKey = strings.ReplaceAll(serverKey, " ", "_")
			for strings.Contains(serverKey, "__") {
				serverKey = strings.ReplaceAll(serverKey, "__", "_")
			}
			serverKey = strings.ToUpper(serverKey)
			if constants[serverKey] == "" {
				constants[serverKey] = server.ID
			}
			for _, channel := range server.Channels {
				if channel.Type != discordgo.ChannelTypeGuildCategory {
					categoryName := ""
					if channel.ParentID != "" {
						channelParent, err := bot.State.Channel(channel.ParentID)
						if err != nil {
							channelParent, err = bot.Channel(channel.ParentID)
						}
						if err == nil {
							categoryName = channelParent.Name
						}
					}
					channelKey := fmt.Sprintf("CHANNEL_%s_%s_%s",
						stripSymbols(server.Name), stripSymbols(categoryName), stripSymbols(channel.Name))
					channelKey = strings.ReplaceAll(channelKey, " ", "_")
					for strings.Contains(channelKey, "__") {
						channelKey = strings.ReplaceAll(channelKey, "__", "_")
					}
					channelKey = strings.ToUpper(channelKey)
					if constants[channelKey] == "" {
						constants[channelKey] = channel.ID
					}
				}
			}
		}
		//--- Save constants
		if _, err := os.Stat(pathConstants); err == nil {
			err = os.Remove(pathConstants)
			if err != nil {
				log.Println(lg("Constants", "", color.HiRedString, "Encountered error deleting cache file:\t%s", err))
			}
		}
		constantsStruct := constants
		newJson, err := json.MarshalIndent(constantsStruct, "", "\t")
		if err != nil {
			log.Println(lg("Constants", "", color.HiRedString, "Failed to format constants...\t%s", err))
		} else {
			err := os.WriteFile(pathConstants, newJson, 0644)
			if err != nil {
				log.Println(lg("Constants", "", color.HiRedString, "Failed to save new constants file...\t%s", err))
			}
		}
	}()

	//#endregion

	//#region Download Emojis & Stickers (after 5s delay)

	runtimeBackgroundWG.Add(1)
	go func() {
		defer runtimeBackgroundWG.Done()
		if waitForRuntimeStop(5 * time.Second) {
			return
		}

		downloadDiscordEmojis()
		if !runtimeIsStopping() {
			downloadDiscordStickers()
		}

	}()

	//#endregion

	//#region <<< BACKGROUND STARTUP COMPLETE >>>

	if config.Verbose {
		log.Println(lg("Verbose", "Startup", color.HiBlueString, "Background task startup finished, took %s...", uptime()))
	}

	//#endregion

	// <<<<<< RUNNING >>>>>>

	//#region ----------- TEST ENV / main

	//#endregion ------------------------

	//#region Exit...
	signal.Notify(loop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, os.Interrupt)
	<-loop
	setGUIRuntimeState("stopping")
	runtimeBackgroundStopOnce.Do(func() { close(runtimeBackgroundStop) })
	runtimeBackgroundWG.Wait()

	sendStatusMessage(sendStatusExit) // not goroutine because we want to wait to send this before logout

	// Log out of twitter if authenticated.
	if twitterScraper != nil {
		if twitterScraper.IsLoggedIn() {
			twitterScraper.Logout()
		}
	}

	historyManagerStopOnce.Do(func() { close(historyManagerStop) })
	historyManagerWG.Wait()
	for _, pair := range historyJobs.Snapshot() {
		job := pair.Value
		if job.Status == historyStatusRunning {
			job.Status = historyStatusAbortRequested
			job.Updated = time.Now()
			historyJobs.Set(pair.Key, job)
		} else if job.Status == historyStatusWaiting {
			job.Status = historyStatusAbortCompleted
			job.Updated = time.Now()
			historyJobs.Set(pair.Key, job)
		}
	}
	historyWorkersWG.Wait()
	if manager := currentDownloadManager(); manager != nil {
		manager.stopAndWait()
	}
	shutdownGUI()
	log.Println(lg("Discord", "", color.GreenString, "Logging out of discord..."))
	if session := currentDiscordSession(); session != nil {
		session.Close()
	}
	if err := saveDuploCatalog(); err != nil {
		log.Println(lg("Duplo", "Shutdown", color.HiRedString, "Error saving duplo catalog: %s", err))
	}

	log.Println(lg("Database", "", color.YellowString, "Closing database..."))
	closeDatabase()

	log.Println(lg("Main", "", color.HiRedString, "Exiting... "))
	//#endregion

}
