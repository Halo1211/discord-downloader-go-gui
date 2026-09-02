package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
)

type historyStatus int

const (
	historyStatusWaiting historyStatus = iota
	historyStatusRunning
	historyStatusAbortRequested
	historyStatusAbortCompleted
	historyStatusErrorReadMessageHistoryPerms
	historyStatusErrorRequesting
	historyStatusCompletedNoMoreMessages
	historyStatusCompletedToBeforeFilter
	historyStatusCompletedToSinceFilter
)

func historyStatusLabel(status historyStatus) string {
	switch status {
	case historyStatusWaiting:
		return "Waiting..."
	case historyStatusRunning:
		return "Currently Downloading..."
	case historyStatusAbortRequested:
		return "Abort Requested..."
	case historyStatusAbortCompleted:
		return "Aborted..."
	case historyStatusErrorReadMessageHistoryPerms:
		return "ERROR: Cannot Read Message History"
	case historyStatusErrorRequesting:
		return "ERROR: Message Requests Failed"
	case historyStatusCompletedNoMoreMessages:
		return "COMPLETE: No More Messages"
	case historyStatusCompletedToBeforeFilter:
		return "COMPLETE: Exceeded Before Date Filter"
	case historyStatusCompletedToSinceFilter:
		return "COMPLETE: Exceeded Since Date Filter"
	default:
		return "Unknown"
	}
}

func historyCacheFilePath(channelID string) string {
	return filepath.Join(pathCacheHistory, safePathComponent(channelID)+".json")
}

func compactHistoryMessages(messages []*discordgo.Message) []*discordgo.Message {
	compacted := messages[:0]
	for _, message := range messages {
		if message != nil {
			compacted = append(compacted, message)
		}
	}
	return compacted
}

func historyAbortRequested(channelID string) bool {
	if runtimeIsStopping() {
		return true
	}
	if historyJobs == nil {
		return false
	}
	job, exists := historyJobs.Get(channelID)
	return exists && job.Status == historyStatusAbortRequested
}

func waitForHistoryRequestDelay(channelID string, duration time.Duration) bool {
	deadline := time.Now().Add(duration)
	for {
		if historyAbortRequested(channelID) {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		if waitForRuntimeStop(remaining) {
			return true
		}
	}
}

type historyJob struct {
	Status                  historyStatus
	OriginUser              string
	OriginChannel           string
	TargetCommandingMessage *discordgo.Message
	TargetChannelID         string
	TargetBefore            string
	TargetSince             string
	DownloadCount           int64
	DownloadSize            int64
	Updated                 time.Time
	Added                   time.Time
}

type historyJobPair struct {
	Key   string
	Value historyJob
}

// historyJobStore serializes access to the job queue. The old ordered map was
// accessed by gateway handlers, the manager, and history workers concurrently.
type historyJobStore struct {
	mu    sync.RWMutex
	order []string
	jobs  map[string]historyJob
}

func newHistoryJobStore() *historyJobStore {
	return &historyJobStore{jobs: make(map[string]historyJob)}
}

func (s *historyJobStore) Get(key string) (historyJob, bool) {
	s.mu.RLock()
	job, exists := s.jobs[key]
	s.mu.RUnlock()
	return job, exists
}

func (s *historyJobStore) Set(key string, job historyJob) {
	s.mu.Lock()
	if _, exists := s.jobs[key]; !exists {
		s.order = append(s.order, key)
	}
	s.jobs[key] = job
	s.mu.Unlock()
}

// Queue installs a new job only when the channel has no active work. Keeping
// the check and write under one lock prevents concurrent commands from
// replacing a waiting/running job with stale parameters.
func (s *historyJobStore) Queue(key string, job historyJob) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, exists := s.jobs[key]; exists {
		switch current.Status {
		case historyStatusWaiting, historyStatusRunning, historyStatusAbortRequested:
			return false
		}
	} else {
		s.order = append(s.order, key)
	}
	job.DownloadCount = 0
	job.DownloadSize = 0
	s.jobs[key] = job
	return true
}

// StartWaiting atomically claims a waiting job. A cancellation that wins the
// lock first therefore cannot be overwritten by a stale manager snapshot.
func (s *historyJobStore) StartWaiting(key string) (historyJob, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[key]
	if !exists || job.Status != historyStatusWaiting {
		return historyJob{}, false
	}
	job.Status = historyStatusRunning
	job.Updated = time.Now()
	s.jobs[key] = job
	return job, true
}

func (s *historyJobStore) RequestCancel(key string) (found, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[key]
	if !exists {
		return false, false
	}
	switch job.Status {
	case historyStatusWaiting:
		job.Status = historyStatusAbortCompleted
	case historyStatusRunning:
		job.Status = historyStatusAbortRequested
	default:
		return true, false
	}
	job.Updated = time.Now()
	s.jobs[key] = job
	return true, true
}

func (s *historyJobStore) SetStatus(key string, status historyStatus) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[key]
	if !exists {
		return false
	}
	job.Status = status
	job.Updated = time.Now()
	s.jobs[key] = job
	return true
}

func (s *historyJobStore) SetProgress(key string, downloads, size int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, exists := s.jobs[key]
	if !exists {
		return false
	}
	job.DownloadCount = downloads
	job.DownloadSize = size
	job.Updated = time.Now()
	s.jobs[key] = job
	return true
}

func (s *historyJobStore) Len() int {
	s.mu.RLock()
	length := len(s.jobs)
	s.mu.RUnlock()
	return length
}

func (s *historyJobStore) Snapshot() []historyJobPair {
	s.mu.RLock()
	result := make([]historyJobPair, 0, len(s.order))
	for _, key := range s.order {
		if job, exists := s.jobs[key]; exists {
			result = append(result, historyJobPair{Key: key, Value: job})
		}
	}
	s.mu.RUnlock()
	return result
}

func setHistoryJobStatus(channelID string, status historyStatus) bool {
	if historyJobs == nil {
		return false
	}
	return historyJobs.SetStatus(channelID, status)
}

func mergeHistoryTerminalStatus(current, candidate historyStatus) historyStatus {
	priority := func(status historyStatus) int {
		switch status {
		case historyStatusAbortCompleted:
			return 6
		case historyStatusErrorReadMessageHistoryPerms:
			return 5
		case historyStatusErrorRequesting:
			return 4
		case historyStatusCompletedToSinceFilter:
			return 3
		case historyStatusCompletedToBeforeFilter:
			return 2
		case historyStatusCompletedNoMoreMessages:
			return 1
		default:
			return 0
		}
	}
	if priority(candidate) > priority(current) {
		return candidate
	}
	return current
}

var (
	historyJobs            *historyJobStore
	historyJobCnt          int
	historyJobCntWaiting   int
	historyJobCntRunning   int
	historyJobCntAborted   int
	historyJobCntErrored   int
	historyJobCntCompleted int
	historyStatsMu         sync.RWMutex
)

func setHistoryJobStats(total, waiting, running, aborted, errored, completed int) {
	historyStatsMu.Lock()
	historyJobCnt = total
	historyJobCntWaiting = waiting
	historyJobCntRunning = running
	historyJobCntAborted = aborted
	historyJobCntErrored = errored
	historyJobCntCompleted = completed
	historyStatsMu.Unlock()
}

func getHistoryJobStats() (total, waiting, running, aborted, errored, completed int) {
	historyStatsMu.RLock()
	total, waiting, running, aborted, errored, completed = historyJobCnt, historyJobCntWaiting, historyJobCntRunning, historyJobCntAborted, historyJobCntErrored, historyJobCntCompleted
	historyStatsMu.RUnlock()
	return
}

// TODO: cleanup
type historyCache struct {
	Updated        time.Time
	Running        bool
	RunningBefore  string // message ID for the next resume request
	CompletedSince string // legacy field retained for old cache-file compatibility
	LatestArchived string // newest message ID fully covered by a completed unfiltered scan
	PendingLatest  string // candidate high-water mark while a scan is still running
}

func processHistoryMessageBatch(messages []*discordgo.Message, channel *discordgo.Channel, workers int) (int64, int64, time.Duration) {
	if len(messages) == 0 {
		return 0, 0, 0
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(messages) {
		workers = len(messages)
	}

	started := time.Now()
	jobs := make(chan *discordgo.Message)
	results := make(chan []downloadedItem, workers)
	var workerWG sync.WaitGroup
	workerWG.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer workerWG.Done()
			for message := range jobs {
				results <- handleMessage(message, channel, false, true)
			}
		}()
	}
	go func() {
		for _, message := range messages {
			jobs <- message
		}
		close(jobs)
		workerWG.Wait()
		close(results)
	}()

	var downloads int64
	var bytes int64
	for items := range results {
		downloads += int64(len(items))
		for _, item := range items {
			bytes += item.Filesize
		}
	}
	if downloads == 0 {
		return 0, 0, 0
	}
	return downloads, bytes, time.Since(started)
}

func effectiveHistoryDownloadWorkers() int {
	workers := config.HistoryDownloadWorkers
	if workers < 1 {
		workers = 1
	}
	if config.ProcessLimit > 0 {
		activeJobs := config.HistoryMaxJobs
		if activeJobs < 1 {
			activeJobs = 1
		}
		perJobLimit := config.ProcessLimit / activeJobs
		if perJobLimit < 1 {
			perJobLimit = 1
		}
		if workers > perJobLimit {
			workers = perJobLimit
		}
	}
	return workers
}

func collectArchivedThreads(fetch func(before *time.Time) (*discordgo.ThreadsList, error)) ([]*discordgo.Channel, error) {
	result := make([]*discordgo.Channel, 0)
	var before *time.Time
	for {
		page, err := fetch(before)
		if err != nil {
			return result, err
		}
		if page == nil {
			return result, errors.New("Discord returned an empty archived-thread response")
		}
		result = append(result, page.Threads...)
		if !page.HasMore {
			return result, nil
		}
		if len(page.Threads) == 0 {
			return result, errors.New("Discord reported more archived threads without a pagination cursor")
		}
		last := page.Threads[len(page.Threads)-1]
		if last == nil || last.ThreadMetadata == nil || last.ThreadMetadata.ArchiveTimestamp.IsZero() {
			return result, errors.New("Discord archived-thread page is missing its pagination timestamp")
		}
		next := last.ThreadMetadata.ArchiveTimestamp
		if before != nil && !next.Before(*before) {
			return result, errors.New("Discord archived-thread pagination cursor did not advance")
		}
		before = &next
	}
}

func handleHistory(commandingMessage *discordgo.Message, subjectChannelID string, before string, since string) int {
	var err error

	historyStartTime := time.Now()

	var historyDownloadDuration time.Duration

	// Log Prefix
	var commander string = "AUTORUN"
	var autorun bool = true
	if commandingMessage != nil { // Only time commandingMessage is nil is Autorun
		if commandingMessage.Author != nil {
			commander = getUserIdentifier(*commandingMessage.Author)
		} else {
			commander = "UNKNOWN"
		}
		autorun = false
	}
	logPrefix := fmt.Sprintf("%s/%s: ", subjectChannelID, commander)

	// Skip Requested
	if historyJobs != nil {
		if job, exists := historyJobs.Get(subjectChannelID); exists &&
			job.Status != historyStatusWaiting && job.Status != historyStatusRunning {
			log.Println(lg("History", "", color.RedString, logPrefix+"History job skipped, Status: %s", historyStatusLabel(job.Status)))
			return -1
		}
	}

	// Vars
	baseChannelInfo, err := bot.State.Channel(subjectChannelID)
	if err != nil {
		baseChannelInfo, err = bot.Channel(subjectChannelID)
		if err != nil {
			log.Println(lg("History", "", color.HiRedString, logPrefix+"Error fetching channel data from discordgo:\t%s", err))
			if job, exists := historyJobs.Get(subjectChannelID); exists {
				job.Status = historyStatusErrorRequesting
				job.Updated = time.Now()
				historyJobs.Set(subjectChannelID, job)
			}
			return -1
		}
	}
	if baseChannelInfo == nil {
		log.Println(lg("History", "", color.HiRedString, "%s", logPrefix+"Discord returned no channel data"))
		if job, exists := historyJobs.Get(subjectChannelID); exists {
			job.Status = historyStatusErrorRequesting
			job.Updated = time.Now()
			historyJobs.Set(subjectChannelID, job)
		}
		return -1
	}

	var totalMessages int64 = 0
	var totalDownloads int64 = 0
	var totalFilesize int64 = 0
	var messageRequestCount int = 0

	sourceMessage := discordgo.Message{} // dummy message
	sourceMessage.ChannelID = subjectChannelID
	sourceMessage.GuildID = baseChannelInfo.GuildID
	sourceConfig := getSource(&sourceMessage)

	var responseMsg *discordgo.Message = nil
	statusChannelID := subjectChannelID
	if commandingMessage != nil {
		statusChannelID = commandingMessage.ChannelID
	}
	sendHistoryEmbed := func(title string, description string) (*discordgo.Message, error) {
		if commandingMessage != nil {
			return replyEmbed(commandingMessage, title, description)
		}
		return bot.ChannelMessageSendEmbed(subjectChannelID, buildEmbed(subjectChannelID, title, description))
	}

	guildName := getServerLabel(baseChannelInfo.GuildID)
	categoryName := getCategoryLabel(baseChannelInfo.ID)

	subjectChannels := []discordgo.Channel{}

	// Check channel type
	baseChannelIsForum := true
	if baseChannelInfo.Type != discordgo.ChannelTypeGuildCategory &&
		baseChannelInfo.Type != discordgo.ChannelTypeGuildForum &&
		baseChannelInfo.Type != discordgo.ChannelTypeGuildStageVoice &&
		baseChannelInfo.Type != discordgo.ChannelTypeGuildVoice &&
		baseChannelInfo.Type != discordgo.ChannelTypeGuildStore {
		subjectChannels = append(subjectChannels, *baseChannelInfo)
		baseChannelIsForum = false
	}

	// Index Threads
	indexedThreads := map[string]bool{}
	if threads, err := bot.ThreadsActive(subjectChannelID); err == nil {
		for _, thread := range threads.Threads {
			if indexedThreads[thread.ID] {
				continue
			}
			subjectChannels = append(subjectChannels, *thread)
			indexedThreads[thread.ID] = true
		}
	}
	archivedThreadSources := []struct {
		name  string
		fetch func(*time.Time) (*discordgo.ThreadsList, error)
	}{
		{name: "public archived", fetch: func(before *time.Time) (*discordgo.ThreadsList, error) {
			return bot.ThreadsArchived(subjectChannelID, before, 100)
		}},
		{name: "private archived", fetch: func(before *time.Time) (*discordgo.ThreadsList, error) {
			return bot.ThreadsPrivateArchived(subjectChannelID, before, 100)
		}},
		{name: "joined private archived", fetch: func(before *time.Time) (*discordgo.ThreadsList, error) {
			return bot.ThreadsPrivateJoinedArchived(subjectChannelID, before, 100)
		}},
	}
	for _, source := range archivedThreadSources {
		threads, fetchErr := collectArchivedThreads(source.fetch)
		for _, thread := range threads {
			if thread == nil {
				continue
			}
			if indexedThreads[thread.ID] {
				continue
			}
			subjectChannels = append(subjectChannels, *thread)
			indexedThreads[thread.ID] = true
		}
		if fetchErr != nil && config.Debug {
			log.Println(lg("Debug", "History", color.YellowString,
				logPrefix+"Could not fully index %s threads: %s", source.name, fetchErr))
		}
	}

	// Send Status?
	var sendStatus bool = true
	if (autorun && !config.SendAutoHistoryStatus) || (!autorun && !config.SendHistoryStatus) {
		sendStatus = false
	}

	// Check Read History perms
	if !baseChannelIsForum && !hasPerms(subjectChannelID, discordgo.PermissionReadMessageHistory) {
		setHistoryJobStatus(subjectChannelID, historyStatusErrorReadMessageHistoryPerms)
		log.Println(lg("History", "", color.HiRedString, "%s", logPrefix+"BOT DOES NOT HAVE PERMISSION TO READ MESSAGE HISTORY!!!"))
		return -1
	}

	// Update Job Status to Downloading
	setHistoryJobStatus(subjectChannelID, historyStatusRunning)
	historyJobs.SetProgress(subjectChannelID, 0, 0)

	//#region Cache Files

	openHistoryCache := func(channel string) historyCache {
		if f, err := os.ReadFile(historyCacheFilePath(channel)); err == nil {
			var ret historyCache
			if err = json.Unmarshal(f, &ret); err != nil {
				log.Println(lg("Debug", "History", color.RedString,
					logPrefix+"Failed to unmarshal json for cache:\t%s", err))
			} else {
				return ret
			}
		}
		return historyCache{}
	}

	writeHistoryCache := func(channel string, cache historyCache) {
		cacheJson, err := json.Marshal(cache)
		if err != nil {
			log.Println(lg("Debug", "History", color.RedString,
				logPrefix+"Failed to format cache into json:\t%s", err))
		} else {
			if err := os.MkdirAll(pathCacheHistory, 0755); err != nil {
				log.Println(lg("Debug", "History", color.HiRedString,
					logPrefix+"Error while creating history cache folder \"%s\": %s", pathCacheHistory, err))
			}
			if err = writeFileAtomic(historyCacheFilePath(channel), cacheJson, 0600); err != nil {
				log.Println(lg("Debug", "History", color.RedString,
					logPrefix+"Failed to write cache file:\t%s", err))
			} else if !autorun && config.Debug {
				log.Println(lg("Debug", "History", color.YellowString, "%s",
					logPrefix+"Wrote to cache file."))
			}
		}
	}

	//#endregion

	finalStatus := historyStatusCompletedNoMoreMessages
	stopAllChannels := false
	for _, channel := range subjectChannels {
		logPrefix = fmt.Sprintf("%s/%s: ", channel.ID, commander)
		channelBefore := before
		channelSince := since
		channelStatus := historyStatusCompletedNoMoreMessages
		useIncrementalCheckpoint := channelBefore == "" && channelSince == ""
		if !hasPerms(channel.ID, discordgo.PermissionReadMessageHistory) {
			channelStatus = historyStatusErrorReadMessageHistoryPerms
			finalStatus = mergeHistoryTerminalStatus(finalStatus, channelStatus)
			log.Println(lg("History", "", color.HiRedString, "%s", logPrefix+"BOT DOES NOT HAVE PERMISSION TO READ MESSAGE HISTORY!!!"))
			continue
		}
		// Invalid Source?
		if sourceConfig == emptySourceConfig {
			log.Println(lg("History", "", color.HiRedString, "%s",
				logPrefix+"Invalid source: "+channel.ID))
			channelStatus = historyStatusErrorRequesting
			finalStatus = mergeHistoryTerminalStatus(finalStatus, channelStatus)
			continue
		} else { // Process
			logHistoryStatus := true
			if sourceConfig.OutputHistoryStatus != nil {
				logHistoryStatus = *sourceConfig.OutputHistoryStatus
			}

			// Overwrite Send Status
			if sourceConfig.SendAutoHistoryStatus != nil {
				if autorun && !*sourceConfig.SendAutoHistoryStatus {
					sendStatus = false
				}
			}
			if sourceConfig.SendHistoryStatus != nil {
				if !autorun && !*sourceConfig.SendHistoryStatus {
					sendStatus = false
				}
			}

			hasPermsToRespond := hasPerms(channel.ID, discordgo.PermissionSendMessages)
			if !autorun {
				hasPermsToRespond = hasPerms(commandingMessage.ChannelID, discordgo.PermissionSendMessages)
			}

			// Date Range Vars
			rangeContent := ""
			var beforeTime time.Time
			var beforeID = channelBefore
			var sinceID = ""

			// Handle Cache File
			channelCache := historyCache{}
			if useIncrementalCheckpoint {
				channelCache = openHistoryCache(channel.ID)
				if channelCache.LatestArchived != "" {
					if config.Debug {
						log.Println(lg("Debug", "History", color.GreenString, "%s",
							logPrefix+"Only checking messages newer than "+channelCache.LatestArchived))
					}
					channelSince = channelCache.LatestArchived
				}
				if channelCache.Running && channelCache.RunningBefore != "" {
					if config.Debug {
						log.Println(lg("Debug", "History", color.YellowString, "%s",
							logPrefix+"Job was interrupted last run, picking up from "+channelCache.RunningBefore))
					}
					beforeID = channelCache.RunningBefore
				}
			}
			completeIncrementalCache := func() {
				if !useIncrementalCheckpoint {
					return
				}
				latest := channelCache.LatestArchived
				if channelCache.PendingLatest != "" {
					latest = channelCache.PendingLatest
				}
				channelCache = historyCache{Updated: time.Now(), LatestArchived: latest}
				writeHistoryCache(channel.ID, channelCache)
			}

			//#region Date Range Output

			var beforeRange = channelBefore
			if beforeRange != "" {
				if isDate(beforeRange) { // user has input YYYY-MM-DD
					beforeRange = discordTimestampToSnowflake("2006-01-02", beforeID)
				} else { // try to parse duration
					dur, err := time.ParseDuration(beforeRange)
					if err == nil {
						beforeRange = discordTimestampToSnowflake("2006-01-02 15:04:05.999999999 -0700 MST", time.Now().Add(-dur).Format("2006-01-02 15:04:05.999999999 -0700 MST"))
					}
				}
				if isNumeric(beforeRange) { // did we convert something (or was it always a number)?
					rangeContent += fmt.Sprintf("**Before:** `%s`\n", beforeRange)
				}
				channelBefore = beforeRange
			}

			var sinceRange = channelSince
			if sinceRange != "" {
				if isDate(sinceRange) { // user has input YYYY-MM-DD
					sinceRange = discordTimestampToSnowflake("2006-01-02", sinceRange)
				} else { // try to parse duration
					dur, err := time.ParseDuration(sinceRange)
					if err == nil {
						sinceRange = discordTimestampToSnowflake("2006-01-02 15:04:05.999999999 -0700 MST", time.Now().Add(-dur).Format("2006-01-02 15:04:05.999999999 -0700 MST"))
					}
				}
				if isNumeric(sinceRange) { // did we convert something (or was it always a number)?
					rangeContent += fmt.Sprintf("**Since:** `%s`\n", sinceRange)
				}
				channelSince = sinceRange
			}

			if rangeContent != "" {
				rangeContent += "\n"
			}

			//#endregion

			channelName := getChannelLabel(channel.ID, &channel)
			if channel.ParentID != "" {
				channelName = getChannelLabel(channel.ParentID, nil) + " \"" + getChannelLabel(channel.ID, &channel) + "\""
			}
			sourceName := fmt.Sprintf("%s / %s", guildName, channelName)
			msgSourceDisplay := fmt.Sprintf("`Server:` **%s**\n`Channel:` #%s", guildName, channelName)
			if categoryName != "unknown" {
				sourceName = fmt.Sprintf("%s / %s / %s", guildName, categoryName, channelName)
				msgSourceDisplay = fmt.Sprintf("`Server:` **%s**\n`Category:` _%s_\n`Channel:` #%s",
					guildName, categoryName, channelName)
			}

			// Initial Status Message
			if sendStatus {
				if hasPermsToRespond {
					responseMsg, err = sendHistoryEmbed("Command — History", msgSourceDisplay)
					if err != nil {
						log.Println(lg("History", "", color.HiRedString,
							logPrefix+"Failed to send command embed message:\t%s", err))
					}
				} else {
					log.Println(lg("History", "", color.HiRedString,
						logPrefix+fmtBotSendPerm, statusChannelID))
				}
			}
			log.Println(lg("History", "", color.HiCyanString, logPrefix+"Began checking history for \"%s\"...", sourceName))

		MessageRequestingLoop:
			for {
				// Next 100
				if beforeTime != (time.Time{}) {
					messageRequestCount++
					if useIncrementalCheckpoint {
						channelCache.Updated = time.Now()
						channelCache.Running = true
						channelCache.RunningBefore = beforeID
						writeHistoryCache(channel.ID, channelCache)
					}

					// Update Status
					if logHistoryStatus {
						log.Println(lg("History", "", color.CyanString,
							logPrefix+"Requesting more, \t%d downloaded (%s), \t%d processed, \tsearching before %s ago (%s)",
							totalDownloads, humanize.Bytes(uint64(totalFilesize)), totalMessages, timeSinceShort(beforeTime), beforeTime.String()[:10]))
					}
					if sendStatus {
						var status string
						if totalDownloads == 0 {
							status = fmt.Sprintf(
								"``%s:`` **No files downloaded...**\n"+
									"_%s messages processed, avg %d msg/s_\n\n"+
									"%s\n\n"+
									"%s`(%d)` _Processing more messages, please wait..._\n",
								timeSinceShort(historyStartTime),
								formatNumber(totalMessages), int(float64(totalMessages)/time.Since(historyStartTime).Seconds()),
								msgSourceDisplay, rangeContent, messageRequestCount,
							)
						} else {
							status = fmt.Sprintf(
								"``%s:`` **%s files downloaded...**\n`%s so far, avg %1.1f MB/s`\n"+
									"_%s messages processed, avg %d msg/s_\n\n"+
									"%s\n\n"+
									"%s`(%d)` _Processing more messages, please wait..._\n",
								timeSinceShort(historyStartTime), formatNumber(totalDownloads),
								humanize.Bytes(uint64(totalFilesize)), float64(totalFilesize/humanize.MByte)/historyDownloadDuration.Seconds(),
								formatNumber(totalMessages), int(float64(totalMessages)/time.Since(historyStartTime).Seconds()),
								msgSourceDisplay, rangeContent, messageRequestCount,
							)
						}
						if !hasPermsToRespond {
							log.Println(lg("History", "", color.HiRedString,
								logPrefix+fmtBotSendPerm+" - %s", statusChannelID))
						} else if responseMsg == nil {
							log.Println(lg("History", "", color.RedString, "%s",
								logPrefix+"Tried to edit status message but it doesn't exist, sending new one."))
							if responseMsg, err = sendHistoryEmbed("Command — History", status); err != nil { // Failed to Edit Status, Send New Message
								log.Println(lg("History", "", color.HiRedString,
									logPrefix+"Failed to send replacement status message:\t%s", err))
							}
						} else {
							// Edit Status
							if selfbot {
								responseMsg, err = bot.ChannelMessageEdit(responseMsg.ChannelID, responseMsg.ID,
									fmt.Sprintf("**Command — History**\n\n%s", status))
							} else {
								responseMsg, err = bot.ChannelMessageEditComplex(&discordgo.MessageEdit{
									ID:      responseMsg.ID,
									Channel: responseMsg.ChannelID,
									Embed:   buildEmbed(responseMsg.ChannelID, "Command — History", status),
								})
							}
							// Failed to Edit Status
							if err != nil {
								log.Println(lg("History", "", color.HiRedString,
									logPrefix+"Failed to edit status message, sending new one:\t%s", err))
								if responseMsg, err = sendHistoryEmbed("Command — History", status); err != nil { // Failed to Edit Status, Send New Message
									log.Println(lg("History", "", color.HiRedString,
										logPrefix+"Failed to send replacement status message:\t%s", err))
								}
							}
						}
					}

					// Update presence
					setLastUpdated(time.Now())
					if *sourceConfig.PresenceEnabled {
						go updateDiscordPresence()
					}
				}

				// Request More Messages
				if config.HistoryRequestDelay > 0 {
					if logHistoryStatus {
						log.Println(lg("History", "", color.YellowString, "Delaying next batch request for %d seconds...", config.HistoryRequestDelay))
					}
					if waitForHistoryRequestDelay(subjectChannelID, safeConfiguredDuration(config.HistoryRequestDelay, time.Second)) {
						channelStatus = historyStatusAbortCompleted
						stopAllChannels = true
						break MessageRequestingLoop
					}
				}
				if messages, fetcherr := bot.ChannelMessages(channel.ID, config.HistoryRequestCount, beforeID, sinceID, ""); fetcherr != nil {
					// Error requesting messages
					if sendStatus {
						if !hasPermsToRespond {
							log.Println(lg("History", "", color.HiRedString,
								logPrefix+fmtBotSendPerm, statusChannelID))
						} else {
							_, senderr := sendHistoryEmbed("Command — History",
								fmt.Sprintf("Encountered an error requesting messages for %s: %s", channel.ID, fetcherr.Error()))
							if senderr != nil {
								log.Println(lg("History", "", color.HiRedString,
									logPrefix+"Failed to send error message:\t%s", senderr))
							}
						}
					}
					log.Println(lg("History", "", color.HiRedString, logPrefix+"Error requesting messages:\t%s", fetcherr))
					channelStatus = historyStatusErrorRequesting
					//TODO: delete cahce or handle it differently?
					break MessageRequestingLoop
				} else {
					messages = compactHistoryMessages(messages)
					// No More Messages
					if len(messages) <= 0 {
						channelStatus = historyStatusCompletedNoMoreMessages
						completeIncrementalCache()
						break MessageRequestingLoop
					}
					if useIncrementalCheckpoint && channelCache.PendingLatest == "" {
						channelCache.PendingLatest = messages[0].ID
					}

					// Set New Range, this shouldn't be changed regardless of before/since filters. The bot will always go latest to oldest.
					beforeID = messages[len(messages)-1].ID
					beforeTime = messages[len(messages)-1].Timestamp
					sinceID = ""

					// Process Messages
					if sourceConfig.HistoryTyping != nil && !autorun {
						if *sourceConfig.HistoryTyping && hasPermsToRespond {
							bot.ChannelTyping(commandingMessage.ChannelID)
						}
					}
					messagesToProcess := make([]*discordgo.Message, 0, len(messages))
					reachedSinceBoundary := false
					for _, message := range messages {
						// Ordered to Cancel
						if job, exists := historyJobs.Get(subjectChannelID); exists {
							if job.Status == historyStatusAbortRequested {
								channelStatus = historyStatusAbortCompleted
								stopAllChannels = true
								break MessageRequestingLoop
							}
						}

						// Check Message Range
						message64, _ := strconv.ParseInt(message.ID, 10, 64)
						if channelBefore != "" {
							before64, _ := strconv.ParseInt(channelBefore, 10, 64)
							if message64 > before64 { // keep scrolling back in messages
								continue
							}
						}
						if channelSince != "" {
							since64, _ := strconv.ParseInt(channelSince, 10, 64)
							if message64 <= since64 { // already archived or older
								channelStatus = historyStatusCompletedToSinceFilter
								reachedSinceBoundary = true
								break
							}
						}
						messagesToProcess = append(messagesToProcess, message)
					}
					downloadCount, downloadSize, downloadDuration := processHistoryMessageBatch(messagesToProcess, &channel, effectiveHistoryDownloadWorkers())
					totalDownloads += downloadCount
					totalFilesize += downloadSize
					historyDownloadDuration += downloadDuration
					totalMessages += int64(len(messagesToProcess))
					historyJobs.SetProgress(subjectChannelID, totalDownloads, totalFilesize)
					if reachedSinceBoundary || len(messages) < config.HistoryRequestCount {
						completeIncrementalCache()
						if !reachedSinceBoundary {
							channelStatus = historyStatusCompletedNoMoreMessages
						}
						break MessageRequestingLoop
					}
				}
			}

			// Final log
			log.Println(lg("History", "", color.HiGreenString, logPrefix+"Finished history for \"%s\", %s files, %s total",
				sourceName, formatNumber(totalDownloads), humanize.Bytes(uint64(totalFilesize))))
			// Final status update
			if sendStatus {
				jobStatus := historyStatusLabel(channelStatus)
				var status string
				if totalDownloads == 0 {
					status = fmt.Sprintf(
						"``%s:`` **No files found...**\n"+
							"_%s total messages processed, avg %d msg/s_\n\n"+
							"%s\n\n"+ // msgSourceDisplay^
							"**DONE!** - %s\n"+
							"Ran ``%d`` message history requests\n\n"+
							"%s_Duration was %s_",
						timeSinceShort(historyStartTime),
						formatNumber(int64(totalMessages)), int(float64(totalMessages)/time.Since(historyStartTime).Seconds()),
						msgSourceDisplay,
						jobStatus,
						messageRequestCount,
						rangeContent, timeSince(historyStartTime),
					)
				} else {
					status = fmt.Sprintf(
						"``%s:`` **%s total files downloaded!**\n`%s total, avg %1.1f MB/s`\n"+
							"_%s total messages processed, avg %d msg/s_\n\n"+
							"%s\n\n"+ // msgSourceDisplay^
							"**DONE!** - %s\n"+
							"Ran ``%d`` message history requests\n\n"+
							"%s_Duration was %s_",
						timeSinceShort(historyStartTime), formatNumber(int64(totalDownloads)),
						humanize.Bytes(uint64(totalFilesize)), float64(totalFilesize/humanize.MByte)/historyDownloadDuration.Seconds(),
						formatNumber(int64(totalMessages)), int(float64(totalMessages)/time.Since(historyStartTime).Seconds()),
						msgSourceDisplay,
						jobStatus,
						messageRequestCount,
						rangeContent, timeSince(historyStartTime),
					)
				}
				if !hasPermsToRespond {
					channelID := subjectChannelID
					if commandingMessage != nil {
						channelID = commandingMessage.ChannelID
					}
					log.Println(lg("History", "", color.HiRedString, logPrefix+fmtBotSendPerm, channelID))
				} else {
					if responseMsg == nil {
						log.Println(lg("History", "", color.RedString, "%s",
							logPrefix+"Tried to edit status message but it doesn't exist, sending new one."))
						if _, err = sendHistoryEmbed("Command — History", status); err != nil { // Failed to Edit Status, Send New Message
							log.Println(lg("History", "", color.HiRedString,
								logPrefix+"Failed to send replacement status message:\t%s", err))
						}
					} else {
						if selfbot {
							responseMsg, err = bot.ChannelMessageEdit(responseMsg.ChannelID, responseMsg.ID,
								fmt.Sprintf("**Command — History**\n\n%s", status))
						} else {
							responseMsg, err = bot.ChannelMessageEditComplex(&discordgo.MessageEdit{
								ID:      responseMsg.ID,
								Channel: responseMsg.ChannelID,
								Embed:   buildEmbed(responseMsg.ChannelID, "Command — History", status),
							})
						}
						// Edit failure
						if err != nil {
							log.Println(lg("History", "", color.RedString,
								logPrefix+"Failed to edit status message, sending new one:\t%s", err))
							if _, err = sendHistoryEmbed("Command — History", status); err != nil {
								log.Println(lg("History", "", color.HiRedString,
									logPrefix+"Failed to send replacement status message:\t%s", err))
							}
						}
					}
				}
			}
			finalStatus = mergeHistoryTerminalStatus(finalStatus, channelStatus)
			if stopAllChannels {
				break
			}
		}
	}
	setHistoryJobStatus(subjectChannelID, finalStatus)

	return int(totalDownloads)
}
