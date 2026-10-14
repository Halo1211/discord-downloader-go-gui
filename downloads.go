package main

import (
	"errors"
	"fmt"
	"image"
	"io"
	"io/fs"
	"log"
	"math"
	"math/rand"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/rivo/duplo"
	"mvdan.cc/xurls/v2"
)

type downloadedItem struct {
	MessageID   string
	URL         string
	Destination string
	Domain      string
	Filesize    int64
}

type downloadItem struct {
	URL         string
	Time        time.Time
	Destination string
	Filename    string
	ChannelID   string
	UserID      string
}

func shouldReactToDownload(historyDownload bool, sourceConfig configurationSource) bool {
	shouldReact := config.ReactWhenDownloaded
	if sourceConfig.ReactWhenDownloaded != nil {
		shouldReact = *sourceConfig.ReactWhenDownloaded
	}
	if historyDownload {
		shouldReact = config.ReactWhenDownloadedHistory
		if sourceConfig.ReactWhenDownloadedHistory != nil {
			shouldReact = *sourceConfig.ReactWhenDownloadedHistory
		}
	}
	return shouldReact
}

func shouldLogLinkStatus(status downloadStatus, logConfig *configurationSourceLog) bool {
	if logConfig == nil {
		return false
	}
	if status > downloadSuccess {
		return logConfig.LogFailures != nil && *logConfig.LogFailures
	}
	return logConfig.LogDownloads != nil && *logConfig.LogDownloads
}

func chooseDownloadReaction(emojis []*discordgo.Emoji, blacklist []string, fallback string) string {
	eligible := make([]string, 0, len(emojis))
	for _, emoji := range emojis {
		if emoji == nil || emoji.Animated {
			continue
		}
		formatted := emoji.APIName()
		if formatted == "" || stringInSlice(formatted, blacklist) {
			continue
		}
		eligible = append(eligible, formatted)
	}
	if len(eligible) == 0 {
		return fallback
	}
	return eligible[rand.Intn(len(eligible))]
}

type downloadStatus int

const (
	downloadSuccess downloadStatus = iota

	downloadIgnored

	downloadSkipped
	downloadSkippedDuplicate
	downloadSkippedUnpermittedDomain
	downloadSkippedUnpermittedExtension
	downloadSkippedUnpermittedFilename
	downloadSkippedUnpermittedReaction
	downloadSkippedUnpermittedType
	downloadSkippedDetectedDuplicate

	downloadFailed
	downloadFailedCode
	downloadFailedCode403
	downloadFailedCode404
	downloadFailedInvalidSource
	downloadFailedInvalidPath
	downloadFailedCreatingFolder
	downloadFailedRequesting
	downloadFailedDownloadingResponse
	downloadFailedReadResponse
	downloadFailedCreatingSubfolder
	downloadFailedWritingFile
	downloadFailedWritingDatabase
	downloadFailedFileTooLarge
)

type downloadStatusStruct struct {
	Status downloadStatus
	Error  error
}

type downloadHTTPStatusError struct {
	statusCode int
	status     string
	retryAfter time.Duration
}

func (e *downloadHTTPStatusError) Error() string { return "HTTP " + e.status }

var downloadHTTPTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 128
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	transport.ForceAttemptHTTP2 = true
	return transport
}()

func responseRetryAfter(response *http.Response) time.Duration {
	for _, header := range []string{"Retry-After", "X-RateLimit-Reset-After"} {
		value := strings.TrimSpace(response.Header.Get(header))
		if value == "" {
			continue
		}
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
			delay := time.Duration(seconds * float64(time.Second))
			if delay <= 0 || delay > 5*time.Minute {
				return 5 * time.Minute
			}
			return delay
		}
		if retryTime, err := http.ParseTime(value); err == nil {
			if delay := time.Until(retryTime); delay > 0 {
				return delay
			}
		}
	}
	return 0
}

func boundedDownloadRetryDelay(delay time.Duration) time.Duration {
	maximum := 30 * time.Second
	if config.DownloadTimeout > 0 {
		timeout := safeConfiguredDuration(config.DownloadTimeout, time.Second)
		if timeout < maximum {
			maximum = timeout
		}
	}
	if delay <= 0 {
		return time.Second
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

func effectiveDownloadRetryAttempts(configured int) int {
	if configured < 1 {
		return 1
	}
	if configured > maxConfigDownloadRetry {
		return maxConfigDownloadRetry
	}
	return configured
}

func validDownloadURL(input string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(input)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("unsupported download URL scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("download URL is missing a host")
	}
	return parsed, nil
}

type fileItem struct {
	Link         string
	Filename     string
	AttachmentID string
	Time         time.Time
}

func mDownloadStatus(status downloadStatus, _error ...error) downloadStatusStruct {
	if len(_error) == 0 {
		return downloadStatusStruct{
			Status: status,
			Error:  nil,
		}
	}
	return downloadStatusStruct{
		Status: status,
		Error:  _error[0],
	}
}

func getDownloadStatus(status downloadStatus) string {
	switch status {
	case downloadSuccess:
		return "Succeeded"
	//
	case downloadIgnored:
		return "Ignored"
	//
	case downloadSkipped:
		return "Skipped"
	case downloadSkippedDuplicate:
		return "Skipped - Duplicate"
	case downloadSkippedUnpermittedDomain:
		return "Skipped - Unpermitted Domain"
	case downloadSkippedUnpermittedExtension:
		return "Skipped - Unpermitted File Extension"
	case downloadSkippedUnpermittedFilename:
		return "Skipped - Unpermitted Filename Content"
	case downloadSkippedUnpermittedReaction:
		return "Skipped - Unpermitted Message Reaction"
	case downloadSkippedUnpermittedType:
		return "Skipped - Unpermitted File Type"
	case downloadSkippedDetectedDuplicate:
		return "Skipped - Detected Duplicate"
	//
	case downloadFailed:
		return "Failed"
	case downloadFailedCode:
		return "Failed - BAD CONNECTION"
	case downloadFailedCode403:
		return "Failed - 403 UNAVAILABLE"
	case downloadFailedCode404:
		return "Failed - 404 NOT FOUND"
	case downloadFailedInvalidSource:
		return "Failed - Invalid Source"
	case downloadFailedInvalidPath:
		return "Failed - Invalid Path"
	case downloadFailedCreatingFolder:
		return "Failed - Error Creating Folder"
	case downloadFailedRequesting:
		return "Failed - Error Requesting"
	case downloadFailedDownloadingResponse:
		return "Failed - Error Downloading Data"
	case downloadFailedReadResponse:
		return "Failed - Error Reading Data"
	case downloadFailedCreatingSubfolder:
		return "Failed - Error Mapping Subfolder(s)"
	case downloadFailedWritingFile:
		return "Failed - Error Saving File"
	case downloadFailedWritingDatabase:
		return "Failed - Error Saving to Database"
	case downloadFailedFileTooLarge:
		return "Failed - File Exceeds Configured Size Limit"
	}
	return "Unknown Error"
}

func getDownloadStatusShort(status downloadStatus) string {
	if status >= downloadFailed {
		return "FAILED"
	} else if status >= downloadSkipped {
		return "SKIPPED"
	} else if status == downloadIgnored {
		return "IGNORED"
	} else if status == downloadSuccess {
		return "DOWNLOADED"
	}
	return "UNKNOWN"
}

// Trim duplicate links in link list
func trimDuplicateLinks(fileItems []*fileItem) []*fileItem {
	var result []*fileItem
	seen := map[string]bool{}

	for _, item := range fileItems {
		if item == nil || item.Link == "" {
			continue
		}
		if seen[item.Link] {
			continue
		}

		seen[item.Link] = true
		result = append(result, item)
	}

	return result
}

// Trim files already downloaded and stored in database
func normalizeDownloadURL(inputURL string) string {
	parsedURL, err := url.Parse(inputURL)
	if err != nil {
		return inputURL
	}
	// URI fragments are client-side navigation metadata and are never sent in
	// an HTTP request, so they cannot distinguish downloaded resources.
	parsedURL.Fragment = ""
	parsedURL.RawFragment = ""
	parsedURL.Scheme = strings.ToLower(parsedURL.Scheme)
	hostname := strings.ToLower(parsedURL.Hostname())
	port := parsedURL.Port()
	if (parsedURL.Scheme == "http" && port == "80") || (parsedURL.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		parsedURL.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsedURL.Host = "[" + hostname + "]"
	} else {
		parsedURL.Host = hostname
	}
	host := strings.ToLower(parsedURL.Hostname())
	if host == "cdn.discordapp.com" || host == "media.discordapp.net" ||
		strings.HasSuffix(host, ".discordapp.com") || strings.HasSuffix(host, ".discordapp.net") {
		// Discord CDN attachment URLs contain expiring ex/is/hm signatures.
		// Keep only the format hint so the same attachment is idempotent across
		// message fetches and refreshed CDN signatures.
		format := parsedURL.Query().Get("format")
		parsedURL.RawQuery = ""
		if format != "" {
			parsedURL.RawQuery = "format=" + url.QueryEscape(format)
		}
	}
	return parsedURL.String()
}

func pruneCompletedLinks(linkList map[string]string, m *discordgo.Message) map[string]string {
	sourceConfig := getSource(m)

	newList := make(map[string]string, 0)
	for link, filename := range linkList {
		testLink := normalizeDownloadURL(link)
		alreadyDownloaded := dbHasDownload(testLink, m.ChannelID)

		savePossibleDuplicates := false
		if sourceConfig.SavePossibleDuplicates != nil {
			savePossibleDuplicates = *sourceConfig.SavePossibleDuplicates
		}

		if !alreadyDownloaded || savePossibleDuplicates {
			newList[link] = filename
		} else if config.Debug {
			log.Println(lg("Download", "SKIP", color.GreenString, "Found URL has already been downloaded for this channel: %s", link))
		}
	}
	return newList
}

func getRawLinks(m *discordgo.Message) []*fileItem {
	var links []*fileItem
	if m == nil {
		return links
	}

	// Search Discord File Attachments
	for _, attachment := range m.Attachments {
		if attachment == nil {
			continue
		}
		links = append(links, &fileItem{
			Link:         attachment.URL,
			Filename:     attachment.Filename,
			AttachmentID: attachment.ID,
		})
	}

	// Search Discord Embedded Content
	for _, embed := range m.Embeds {
		if embed == nil {
			continue
		}
		if embed.URL != "" {
			links = append(links, &fileItem{
				Link: embed.URL,
			})
		}

		if embed.Image != nil && embed.Image.URL != "" {
			links = append(links, &fileItem{
				Link: embed.Image.URL,
			})
		}

		if embed.Video != nil && embed.Video.URL != "" {
			links = append(links, &fileItem{
				Link: embed.Video.URL,
			})
		}

		// Description checking removed because it causes absolute chaos,
		// fetching every random link from the description of things like YouTube videos.
		/*if embed.Description != "" {
			foundLinks = xurls.Strict().FindAllString(embed.Description, -1)
			for _, foundLink := range foundLinks {
				links = append(links, &fileItem{
					Link: foundLink,
				})
			}
		}*/
	}

	// Search Discord Message Snapshots (forwarded messages)
	for _, snapshot := range m.MessageSnapshots {
		if snapshot.Message == nil {
			continue
		}
		// Attachments
		for _, attachment := range snapshot.Message.Attachments {
			if attachment == nil {
				continue
			}
			links = append(links, &fileItem{
				Link:         attachment.URL,
				Filename:     attachment.Filename,
				AttachmentID: attachment.ID,
			})
		}
		// Embeds
		for _, embed := range snapshot.Message.Embeds {
			if embed == nil {
				continue
			}
			if embed.URL != "" {
				links = append(links, &fileItem{
					Link: embed.URL,
				})
			}

			if embed.Image != nil && embed.Image.URL != "" {
				links = append(links, &fileItem{
					Link: embed.Image.URL,
				})
			}

			if embed.Video != nil && embed.Video.URL != "" {
				links = append(links, &fileItem{
					Link: embed.Video.URL,
				})
			}
		}
	}

	// Search Detected Links
	foundLinks := xurls.Strict().FindAllString(m.Content, -1)
	for _, foundLink := range foundLinks {
		links = append(links, &fileItem{
			Link: foundLink,
		})
	}

	// Search Detected Links in Discord Message Snapshots (forwarded messages)
	for _, snapshot := range m.MessageSnapshots {
		if snapshot.Message == nil {
			continue
		}
		foundLinks := xurls.Strict().FindAllString(snapshot.Message.Content, -1)
		for _, foundLink := range foundLinks {
			links = append(links, &fileItem{
				Link: foundLink,
			})
		}
	}

	return links
}

func getParsedLinks(inputURL string, m *discordgo.Message) map[string]string {
	/* TODO: Download Support...
	- TikTok: Tried, once the connection is closed the cdn URL is rendered invalid
	- Facebook Photos: Tried, it doesn't preload image data, it's loaded in after. Would have to keep connection open, find alternative way to grab, or use api.
	- Facebook Videos: Previously supported but they split mp4 into separate audio and video streams
	*/

	// Twitter / X
	if parsed, err := url.Parse(inputURL); err == nil {
		switch strings.ToLower(parsed.Hostname()) {
		case "mobile.twitter.com", "fxtwitter.com", "c.vxtwitter.com", "vxtwitter.com", "x.com", "www.x.com":
			port := parsed.Port()
			parsed.Host = "twitter.com"
			if port != "" {
				parsed.Host = net.JoinHostPort("twitter.com", port)
			}
			inputURL = parsed.String()
		}
	}
	// Direct Twitter CDN media does not require an authenticated scraper.
	if regexUrlTwitter.MatchString(inputURL) {
		links, err := getTwitterUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Twitter Media fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	if twitterConnected.Load() && regexUrlTwitterStatus.MatchString(inputURL) {
		links, err := getTwitterStatusUrls(inputURL, m)
		if err != nil {
			if !strings.Contains(err.Error(), "suspended") && !strings.Contains(err.Error(), "No status found") {
				log.Println(lg("Download", "", color.RedString, "Twitter Status fetch failed for %s -- %s", inputURL, err))
			}
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	} else if regexUrlTwitterStatus.MatchString(inputURL) {
		// Do not save an X/Twitter HTML page as if it were archived media.
		return map[string]string{}
	}

	// Instagram
	if regexUrlInstagram.MatchString(inputURL) || regexUrlInstagramReel.MatchString(inputURL) || regexUrlInstagramTV.MatchString(inputURL) {
		if !instagramConnected.Load() {
			return map[string]string{}
		}
		links, err := getInstagramUrls(inputURL, m)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Instagram media fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
		// Do not save an Instagram HTML page when media extraction fails.
		return map[string]string{}
	}

	// Imgur (Legacy from DIDG)
	if regexUrlImgurSingle.MatchString(inputURL) {
		links, err := getImgurSingleUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Imgur Media fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	if regexUrlImgurAlbum.MatchString(inputURL) {
		links, err := getImgurAlbumUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Imgur Album fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Streamable (Legacy from DIDG)
	if regexUrlStreamable.MatchString(inputURL) {
		links, err := getStreamableUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Streamable fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Gfycat (Legacy from DIDG)
	if regexUrlGfycat.MatchString(inputURL) {
		links, err := getGfycatUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Gfycat fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Flickr (Legacy from DIDG)
	if regexUrlFlickrPhoto.MatchString(inputURL) {
		links, err := getFlickrPhotoUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Flickr Photo fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	if regexUrlFlickrAlbum.MatchString(inputURL) {
		links, err := getFlickrAlbumUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Flickr Album fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	if regexUrlFlickrAlbumShort.MatchString(inputURL) {
		links, err := getFlickrAlbumShortUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Flickr Album (short) fetch failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Tistory (Legacy from DIDG)
	if regexUrlTistory.MatchString(inputURL) {
		links, err := getTistoryUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Tistory URL failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	if regexUrlTistoryLegacy.MatchString(inputURL) {
		links, err := getLegacyTistoryUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Legacy Tistory URL failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}
	// The original project has this as an option,
	if regexUrlPossibleTistorySite.MatchString(inputURL) {
		links, err := getPossibleTistorySiteUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Checking for Tistory site failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Reddit
	if regexUrlRedditPost.MatchString(inputURL) {
		links, err := getRedditPostUrls(inputURL)
		if err != nil {
			log.Println(lg("Download", "", color.RedString, "Reddit Post URL failed for %s -- %s", inputURL, err))
		} else if len(links) > 0 {
			return pruneCompletedLinks(links, m)
		}
	}

	// Try without queries
	/*parsedURL, err := url.Parse(inputURL)
	if err == nil {
		if strings.Contains(parsedURL.String(), "format=") {
			parsedURL.RawQuery = "format=" + parsedURL.Query().Get("format")
		} else {
			parsedURL.RawQuery = ""
		}
		inputURLWithoutQueries := parsedURL.String()
		if inputURLWithoutQueries != inputURL {
			return pruneCompletedLinks(getParsedLinks(inputURLWithoutQueries, m), m)
		}
	}*/

	return pruneCompletedLinks(map[string]string{inputURL: ""}, m)
}

func getLinksByMessage(m *discordgo.Message) []*fileItem {
	var fileItems []*fileItem
	if m == nil {
		return fileItems
	}

	linkTime := m.Timestamp

	rawLinks := getRawLinks(m)
	for _, rawLink := range rawLinks {
		downloadLinks := getParsedLinks(rawLink.Link, m)
		for link, filename := range downloadLinks {
			if rawLink.Filename != "" {
				filename = rawLink.Filename
			}

			fileItems = append(fileItems, &fileItem{
				Link:         link,
				Filename:     filename,
				Time:         linkTime,
				AttachmentID: rawLink.AttachmentID,
			})
		}
	}

	// Scrape Emojis from message
	for _, emoji := range m.GetCustomEmojis() {
		emojisWEBP := false
		if config.IgnoreEmojisWEBP != nil {
			emojisWEBP = *config.IgnoreEmojisWEBP
		}

		link := "https://cdn.discordapp.com/emojis/" + emoji.ID + ".png"
		if emojisWEBP {
			link = "https://cdn.discordapp.com/emojis/" + emoji.ID + ".webp"
		}
		if emoji.Animated {
			link = "https://cdn.discordapp.com/emojis/" + emoji.ID + ".gif"
			if emojisWEBP {
				link = "https://cdn.discordapp.com/emojis/" + emoji.ID + ".webp?animated=true"
			}
		}

		fileItems = append(fileItems, &fileItem{
			Link:         link,
			Filename:     "Emoji " + emoji.Name,
			Time:         linkTime,
			AttachmentID: "",
		})
	}

	// Scrape Stickers from message
	for _, sticker := range m.StickerItems {
		if sticker == nil {
			continue
		}
		fileItems = append(fileItems, &fileItem{
			Link:         "https://media.discordapp.net/stickers/" + sticker.ID + ".png",
			Filename:     "Sticker " + sticker.Name,
			Time:         linkTime,
			AttachmentID: "",
		})
	}

	return trimDuplicateLinks(fileItems)
}

type downloadRequestStruct struct {
	InputURL       string
	Filename       string
	Extension      string
	Path           string
	Message        *discordgo.Message
	Channel        *discordgo.Channel
	FileTime       time.Time
	HistoryCmd     bool
	EmojiCmd       bool
	ManualDownload bool
	StartTime      time.Time
	AttachmentID   string
}

var downloadWriteMu sync.Mutex
var cachedDownloadIDMu sync.Mutex

func nextCachedDownloadID() int {
	cachedDownloadIDMu.Lock()
	cachedDownloadID++
	id := cachedDownloadID
	cachedDownloadIDMu.Unlock()
	return id
}

func setCachedDownloadID(value int) {
	cachedDownloadIDMu.Lock()
	cachedDownloadID = value
	cachedDownloadIDMu.Unlock()
}

func writeFileAtomic(filename string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(filename), "."+filepath.Base(filename)+".*.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if err := tmp.Chmod(mode); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, filename); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

func downloadDomainAllowed(domain string, filters *configurationSourceFilters) bool {
	if filters == nil {
		return true
	}
	domain = strings.ToLower(domain)
	if filters.BlockedDomains != nil {
		for _, blocked := range *filters.BlockedDomains {
			if strings.EqualFold(domain, blocked) {
				return false
			}
		}
	}
	if filters.AllowedDomains != nil {
		for _, allowed := range *filters.AllowedDomains {
			if strings.EqualFold(domain, allowed) {
				return true
			}
		}
		return false
	}
	return true
}

func pathIsWithinBase(basePath, targetPath string) bool {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func downloadLinkContentAllowed(link string, filters *configurationSourceFilters) bool {
	if filters == nil {
		return true
	}
	if filters.BlockedLinkContent != nil {
		for _, phrase := range *filters.BlockedLinkContent {
			if phrase != "" && strings.Contains(link, phrase) {
				return false
			}
		}
	}
	if filters.AllowedLinkContent != nil {
		for _, phrase := range *filters.AllowedLinkContent {
			if phrase != "" && strings.Contains(link, phrase) {
				return true
			}
		}
		return false
	}
	return true
}

func formattedDownloadSubfolders(sourceConfig configurationSource, download downloadRequestStruct, contentTypeBase string) []string {
	if sourceConfig.Subfolders == nil {
		return nil
	}
	format := func(template string) string {
		formatted := strings.ReplaceAll(template, "{{fileType}}", contentTypeBase+"s")
		formatted = dataKeys_DiscordMessage(formatted, download.Message)
		return safePathComponent(clearSourceField(formatted, sourceConfig))
	}

	subfolders := make([]string, 0, len(*sourceConfig.Subfolders))
	for _, subfolder := range *sourceConfig.Subfolders {
		formatted := format(subfolder)
		if strings.Contains(formatted, "{{") && strings.Contains(formatted, "}}") &&
			sourceConfig.SubfoldersFallback != nil {
			subfolders = make([]string, 0, len(*sourceConfig.SubfoldersFallback))
			for _, fallback := range *sourceConfig.SubfoldersFallback {
				subfolders = append(subfolders, format(fallback))
			}
			break
		}
		subfolders = append(subfolders, formatted)
	}
	return subfolders
}

func (download downloadRequestStruct) handleDownload() (downloadStatusStruct, int64) {
	status := mDownloadStatus(downloadFailed)
	var tempfilesize int64 = -1
	attempts := effectiveDownloadRetryAttempts(config.DownloadRetryMax)
	for i := 0; i < attempts; i++ {
		status, tempfilesize = download.tryDownload()
		if status.Status < downloadFailed || !downloadStatusRetryable(status.Status) || i == attempts-1 {
			break
		}
		// Exponential backoff with jitter prevents a group of failed downloads
		// from retrying in lockstep and gives transient CDN failures time to heal.
		var httpStatusError *downloadHTTPStatusError
		var delay time.Duration
		if errors.As(status.Error, &httpStatusError) && httpStatusError.retryAfter > 0 {
			delay = httpStatusError.retryAfter
		} else {
			backoff := time.Duration(1<<min(i, 5)) * time.Second
			jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
			delay = backoff + jitter
		}
		if waitForRuntimeStop(boundedDownloadRetryDelay(delay)) {
			return status, tempfilesize
		}
	}
	if runtimeIsStopping() {
		return status, tempfilesize
	}

	// Any kind of failure
	if status.Status >= downloadFailed && !download.HistoryCmd && !download.EmojiCmd {
		log.Println(lg("Download", "", color.RedString,
			"Gave up on downloading %s after %d failed attempts...\t%s",
			download.InputURL, attempts, getDownloadStatus(status.Status)))
		if sourceConfig := getSource(download.Message); sourceConfig != emptySourceConfig {
			if !download.HistoryCmd && *sourceConfig.SendErrorMessages {
				content := fmt.Sprintf(
					"Gave up trying to download\n<%s>\nafter %d failed attempts...\n\n``%s``",
					download.InputURL, attempts, getDownloadStatus(status.Status))
				if status.Error != nil {
					content += fmt.Sprintf("\n```ERROR: %s```", status.Error)
				}
				// Failure Notice
				session := currentDiscordSession()
				if session == nil || !hasPerms(download.Message.ChannelID, discordgo.PermissionSendMessages) {
					log.Println(lg("Download", "", color.HiRedString, fmtBotSendPerm, download.Message.ChannelID))
				} else {
					if selfbot {
						_, err := session.ChannelMessageSend(download.Message.ChannelID,
							fmt.Sprintf("%s **Download Failure**\n\n%s", download.Message.Author.Mention(), content))
						if err != nil {
							log.Println(lg("Download", "", color.HiRedString,
								"Failed to send failure message to %s: %s", download.Message.ChannelID, err))
						}
					} else {
						if _, err := session.ChannelMessageSendComplex(download.Message.ChannelID,
							&discordgo.MessageSend{
								Content: fmt.Sprintf("<@!%s>", download.Message.Author.ID),
								Embed:   buildEmbed(download.Message.ChannelID, "Download Failure", content),
							}); err != nil {
							log.Println(lg("Download", "", color.HiRedString,
								"Failed to send failure message to %s: %s",
								download.Message.ChannelID, err))
						}
					}
				}
			}
			if status.Error != nil {
				sendErrorMessage(fmt.Sprintf("**%s**\n\n%s", getDownloadStatus(status.Status), status.Error))
			}
		}
	}

	// Log Links to File
	if !download.EmojiCmd {
		if sourceConfig := getSource(download.Message); sourceConfig != emptySourceConfig {
			if sourceConfig.LogLinks != nil {
				if sourceConfig.LogLinks.Destination != "" {

					encounteredErrors := false
					savePath := filepath.Clean(sourceConfig.LogLinks.Destination)

					// Subfolder Division - Format Subfolders
					if sourceConfig.LogLinks.Subfolders != nil {
						subfolders := []string{}
						for _, subfolder := range *sourceConfig.LogLinks.Subfolders {
							newSubfolder := dataKeys_DiscordMessage(
								dataKeys_DownloadStatus(subfolder, status, download),
								download.Message)

							// Scrub subfolder
							newSubfolder = clearSourceLogField(newSubfolder, *sourceConfig.LogLinks)

							// Do Fallback if a line contains an unparsed key (if fallback exists).
							if strings.Contains(newSubfolder, "{{") && strings.Contains(newSubfolder, "}}") &&
								sourceConfig.LogLinks.SubfoldersFallback != nil {
								subfolders = []string{}
								for _, subfolder2 := range *sourceConfig.LogLinks.SubfoldersFallback {
									newSubfolder2 := dataKeys_DiscordMessage(
										dataKeys_DownloadStatus(subfolder2, status, download),
										download.Message)

									// Scrub subfolder
									newSubfolder2 = clearSourceLogField(newSubfolder2, *sourceConfig.LogLinks)

									subfolders = append(subfolders, safePathComponent(newSubfolder2))
								}
								break
							} else {
								subfolders = append(subfolders, safePathComponent(newSubfolder))
							}
						}

						// Subfolder Division - Handle Formatted Subfolders
						var pathErr error
						savePath, pathErr = safePathUnderBase(savePath, subfolders...)
						if pathErr != nil {
							log.Println(lg("LogLinks", "", color.HiRedString, "Invalid log subfolder path: %s", pathErr))
							encounteredErrors = true
						} else if err := os.MkdirAll(savePath, 0755); err != nil {
							log.Println(lg("LogLinks", "", color.HiRedString,
								"Error while creating subfolder \"%s\": %s", savePath, err))
							encounteredErrors = true
						}
					}

					if !encounteredErrors {
						if _, err := os.Stat(savePath); err != nil {
							log.Println(lg("Download", "LogLinks", color.HiRedString,
								"Save path %s is invalid... %s", savePath, err))
						} else {
							// Format filename
							filename := download.Message.ChannelID + ".txt"
							if sourceConfig.LogLinks.FilenameFormat != nil {
								if *sourceConfig.LogLinks.FilenameFormat != "" {
									filename = dataKeys_DiscordMessage(
										dataKeys_DownloadStatus(*sourceConfig.LogLinks.FilenameFormat, status, download),
										download.Message)
									// if extension presumed missing
									if !strings.Contains(filename, ".") {
										filename += ".txt"
									}
								}
							}

							// Scrub filename
							filename = safePathComponent(clearSourceLogField(filename, *sourceConfig.LogLinks))

							// Build path
							logPath, pathErr := safePathUnderBase(savePath, filename)
							if pathErr != nil {
								log.Println(lg("Download", "LogLinks", color.HiRedString, "Invalid log filename: %s", pathErr))
								return status, tempfilesize
							}

							// Format New Line
							var newLine string
							// Prepend
							prefix := ""
							if sourceConfig.LogLinks.LinePrefix != nil {
								prefix = *sourceConfig.LogLinks.LinePrefix
							}
							prefix = dataKeys_DiscordMessage(
								dataKeys_DownloadStatus(prefix, status, download),
								download.Message)

							// Append
							suffix := ""
							if sourceConfig.LogLinks.LineSuffix != nil {
								suffix = *sourceConfig.LogLinks.LineSuffix
							}
							suffix = dataKeys_DiscordMessage(
								dataKeys_DownloadStatus(suffix, status, download),
								download.Message)
							// New Line
							lineContent := download.InputURL
							if sourceConfig.LogLinks.LineContent != nil {
								lineContent = *sourceConfig.LogLinks.LineContent
							}
							// Message content
							msgContent := download.Message.Content
							if session := currentDiscordSession(); session != nil {
								if contentFmt, err := download.Message.ContentWithMoreMentionsReplaced(session); err == nil {
									msgContent = contentFmt
								}
							}
							keys := [][]string{
								{"{{link}}", download.InputURL},
								{"{{msgContent}}", msgContent},
							}
							for _, embed := range download.Message.Embeds {
								if embed != nil {
									keys = append(keys, []string{"{{embedDesc}}", embed.Description})
									break
								}
							}
							for _, key := range keys {
								if strings.Contains(lineContent, key[0]) {
									lineContent = strings.ReplaceAll(lineContent, key[0], key[1])
								}
							}
							newLine += "\n" + prefix + lineContent + suffix

							canLog := shouldLogLinkStatus(status.Status, sourceConfig.LogLinks)
							filterDuplicates := sourceConfig.LogLinks.FilterDuplicates != nil && *sourceConfig.LogLinks.FilterDuplicates
							if _, err := appendSourceLogLine(logPath, newLine, filterDuplicates, canLog); err != nil {
								log.Println(lg("Download", "LogLinks", color.RedString, "[sourceConfig.LogLinks] Failed to append log file:\t%s", err))
							}
						}
					}
				}
			}
		}
	}

	return status, tempfilesize
}

func downloadStatusRetryable(status downloadStatus) bool {
	switch status {
	case downloadFailedCode, downloadFailedRequesting,
		downloadFailedDownloadingResponse, downloadFailedReadResponse,
		downloadFailedWritingFile:
		return true
	default:
		return false
	}
}

func (download downloadRequestStruct) tryDownload() (downloadStatusStruct, int64) {
	var err error
	if !download.EmojiCmd && download.Message == nil {
		return mDownloadStatus(downloadFailedInvalidSource, errors.New("download request is missing its Discord message")), 0
	}

	downloadID := nextCachedDownloadID()
	duploReserved := false
	duploCommitted := false
	defer func() {
		if !duploReserved || duploCommitted {
			return
		}
		duploMu.Lock()
		if duploCatalog != nil {
			duploCatalog.Delete(downloadID)
		}
		duploMu.Unlock()
	}()

	logPrefix := ""
	if download.HistoryCmd {
		logPrefix = "HISTORY "
	}

	var fileinfo fs.FileInfo

	var sourceConfig configurationSource
	sourceDefault(&sourceConfig)
	sourceConfigNew := emptySourceConfig
	if !download.EmojiCmd {
		sourceConfigNew = getSource(download.Message)
	}
	if sourceConfigNew != emptySourceConfig { // this looks stupid but it has a purpose.
		sourceConfig = sourceConfigNew
	}
	if sourceConfigNew != emptySourceConfig || download.EmojiCmd || download.ManualDownload {

		// Source validation
		parsedURL, parseErr := validDownloadURL(download.InputURL)
		if parseErr != nil {
			return mDownloadStatus(downloadFailedInvalidSource, parseErr), 0
		}

		// Check Domain
		domain := parsedURL.Hostname()
		if !downloadDomainAllowed(domain, sourceConfig.Filters) {
			if !download.HistoryCmd {
				log.Println(lg("Download", "Skip", color.GreenString,
					"Unpermitted domain (%s) found at %s", domain, download.InputURL))
			}
			return mDownloadStatus(downloadSkippedUnpermittedDomain), 0
		}

		// Clean/fix path
		if download.Path == "" || download.Path == string(os.PathSeparator) {
			log.Println(lg("Download", "", color.HiRedString, "Destination cannot be empty path..."))
			return mDownloadStatus(downloadFailedInvalidPath, err), 0
		}
		if !strings.HasSuffix(download.Path, string(os.PathSeparator)) {
			download.Path = download.Path + string(os.PathSeparator)
		}

		// Create folder
		if err = os.MkdirAll(download.Path, 0755); err != nil {
			log.Println(lg("Download", "", color.HiRedString,
				"Error while creating destination folder \"%s\": %s",
				download.Path, err))
			return mDownloadStatus(downloadFailedCreatingFolder, err), 0
		}

		// Request
		timeout := safeConfiguredDuration(config.DownloadTimeout, time.Second)
		client := &http.Client{Timeout: timeout, Transport: downloadHTTPTransport}
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) > config.DownloadMaxRedirects {
				return fmt.Errorf("too many redirects (limit %d)", config.DownloadMaxRedirects)
			}
			redirectDomain := strings.ToLower(request.URL.Hostname())
			if !downloadDomainAllowed(redirectDomain, sourceConfig.Filters) {
				return fmt.Errorf("redirected to unpermitted domain %s", redirectDomain)
			}
			return nil
		}
		request, err := http.NewRequest(http.MethodGet, download.InputURL, nil)
		if err != nil {
			log.Println(lg("Download", "", color.HiRedString, "Error while requesting \"%s\": %s", download.InputURL, err))
			return mDownloadStatus(downloadFailedRequesting, err), 0
		}
		request.Header.Set("User-Agent", sneakyUserAgent)
		request.Header.Add("Accept-Encoding", "identity")
		response, err := client.Do(request)
		if err != nil {
			if !strings.Contains(err.Error(), "no such host") && !strings.Contains(err.Error(), "connection refused") {
				log.Println(lg("Download", "", color.HiRedString,
					"Error while receiving response from \"%s\": %s",
					download.InputURL, err))
			}
			return mDownloadStatus(downloadFailedDownloadingResponse, err), 0
		}
		defer response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			statusErr := &downloadHTTPStatusError{
				statusCode: response.StatusCode,
				status:     response.Status,
				retryAfter: responseRetryAfter(response),
			}
			logHistoryErrors := true
			if sourceConfig.OutputHistoryErrors != nil {
				logHistoryErrors = *sourceConfig.OutputHistoryErrors
			}
			if logHistoryErrors {
				log.Println(lg("Download", "", color.HiRedString, logPrefix+"DOWNLOAD FAILED, %d %s: %s",
					response.StatusCode, http.StatusText(response.StatusCode), download.InputURL))
			}
			switch response.StatusCode {
			case http.StatusForbidden:
				return mDownloadStatus(downloadFailedCode403, statusErr), 0
			case http.StatusNotFound:
				return mDownloadStatus(downloadFailedCode404, statusErr), 0
			default:
				return mDownloadStatus(downloadFailedCode, statusErr), 0
			}
		}

		// Stream the response to a temporary file so large downloads do not occupy
		// the process heap. The file is removed unless it is atomically promoted.
		tempFile, err := os.CreateTemp(download.Path, ".ddg-download-*.part")
		if err != nil {
			return mDownloadStatus(downloadFailedWritingFile, err), 0
		}
		tempDownloadPath := tempFile.Name()
		defer os.Remove(tempDownloadPath)
		if response.ContentLength > config.DownloadMaxFileSize {
			tempFile.Close()
			return mDownloadStatus(downloadFailedFileTooLarge,
				fmt.Errorf("content length %d exceeds limit %d", response.ContentLength, config.DownloadMaxFileSize)), 0
		}
		responseReader := io.Reader(response.Body)
		if config.DownloadMaxFileSize < math.MaxInt64 {
			responseReader = io.LimitReader(response.Body, config.DownloadMaxFileSize+1)
		}
		bytesWritten, err := io.Copy(tempFile, responseReader)
		if err != nil {
			tempFile.Close()
			log.Println(lg("Download", "", color.HiRedString,
				"Could not read response from \"%s\": %s",
				download.InputURL, err))
			return mDownloadStatus(downloadFailedReadResponse, err), 0
		}
		if err := tempFile.Close(); err != nil {
			return mDownloadStatus(downloadFailedReadResponse, err), 0
		}
		if bytesWritten > config.DownloadMaxFileSize {
			return mDownloadStatus(downloadFailedFileTooLarge,
				fmt.Errorf("response exceeds limit %d bytes", config.DownloadMaxFileSize)), 0
		}

		// Content Type
		headFile, err := os.Open(tempDownloadPath)
		if err != nil {
			return mDownloadStatus(downloadFailedReadResponse, err), 0
		}
		head := make([]byte, 512)
		headCount, headErr := headFile.Read(head)
		headFile.Close()
		if headErr != nil && headErr != io.EOF {
			return mDownloadStatus(downloadFailedReadResponse, headErr), 0
		}
		contentType := http.DetectContentType(head[:headCount])
		contentTypeParts := strings.Split(contentType, "/")
		contentTypeBase := contentTypeParts[0]
		isHtml := strings.Contains(contentType, "text/html")

		// Filename
		if download.Filename == "" {
			download.Filename = filenameFromURL(response.Request.URL.String())
			if disposition := response.Header.Get("Content-Disposition"); disposition != "" {
				if _, params, err := mime.ParseMediaType(disposition); err == nil {
					// mime.ParseMediaType already decodes RFC 5987 filename* values.
					// Decoding again corrupts literal percent sequences such as %20;
					// regular filename values are not URL-encoded by the standard.
					newFilename := params["filename"]
					if newFilename != "" {
						download.Filename = newFilename
					}
				}
			}
		}

		// Check Filename
		if sourceConfig.Filters.AllowedFilenames != nil || sourceConfig.Filters.BlockedFilenames != nil {
			shouldAbort := false

			if sourceConfig.Filters.BlockedFilenames != nil {
				for _, phrase := range *sourceConfig.Filters.BlockedFilenames {
					if phrase != "" && phrase != " " && strings.Contains(download.Filename, phrase) {
						shouldAbort = true
					}
				}
			}
			if sourceConfig.Filters.AllowedFilenames != nil {
				allowed := false
				for _, phrase := range *sourceConfig.Filters.AllowedFilenames {
					if phrase != "" && phrase != " " && strings.Contains(download.Filename, phrase) {
						allowed = true
					}
				}
				shouldAbort = shouldAbort || !allowed
			}

			// Abort
			if shouldAbort {
				if !download.HistoryCmd {
					log.Println(lg("Download", "Skip", color.GreenString,
						"Unpermitted filename content \"%s\"", download.Filename))
				}
				return mDownloadStatus(downloadSkippedUnpermittedFilename), 0
			}
		}

		// Check Reactions
		if download.Message != nil &&
			(sourceConfig.Filters.AllowedReactions != nil || sourceConfig.Filters.BlockedReactions != nil) {
			shouldAbort := false
			allowed := sourceConfig.Filters.AllowedReactions == nil

			if download.Message.Reactions != nil {
				for _, reaction := range download.Message.Reactions {
					if reaction == nil || reaction.Emoji == nil {
						continue
					}
					if sourceConfig.Filters.BlockedReactions != nil {
						if stringInSlice(reaction.Emoji.ID, *sourceConfig.Filters.BlockedReactions) ||
							stringInSlice(reaction.Emoji.Name, *sourceConfig.Filters.BlockedReactions) {
							shouldAbort = true
						}
					}
					if sourceConfig.Filters.AllowedReactions != nil {
						if stringInSlice(reaction.Emoji.ID, *sourceConfig.Filters.AllowedReactions) ||
							stringInSlice(reaction.Emoji.Name, *sourceConfig.Filters.AllowedReactions) {
							allowed = true
						}
					}
				}
			}
			shouldAbort = shouldAbort || !allowed

			// Abort
			if shouldAbort {
				if !download.HistoryCmd {
					log.Println(lg("Download", "Skip", color.GreenString,
						"Did not meet reaction filter criteria"))
				}
				return mDownloadStatus(downloadSkippedUnpermittedReaction), 0
			}
		}

		// Extension
		download.Extension = strings.ToLower(filepath.Ext(download.Filename))
		if filepath.Ext(download.Filename) == "" {
			if possibleExtension, _ := mime.ExtensionsByType(contentType); len(possibleExtension) > 0 {
				download.Filename += possibleExtension[0]
				download.Extension = possibleExtension[0]
			}
		}

		// Format Keys
		if !download.EmojiCmd {
			download.Filename = dataKeysDownload("", sourceConfig, download, true)
		}

		// Scrub Filename
		download.Filename = safePathComponent(clearSourceField(download.Filename, sourceConfig))

		// Keep the final path component portable and never split Unicode bytes.
		download.Filename = limitFilenameBytes(download.Filename, 255)

		// Swap Extensions
		if download.Extension == ".jfif" || download.Extension == ".jpglarge" || download.Extension == ".jpeglarge" {
			download.Extension = ".jpg"
			download.Filename = strings.TrimSuffix(download.Filename, filepath.Ext(download.Filename)) + download.Extension
		}

		// Fix content type using extension
		if stringInSlice(download.Extension, []string{".mov"}) ||
			stringInSlice(download.Extension, []string{".mp4"}) ||
			stringInSlice(download.Extension, []string{".webm"}) {
			contentTypeBase = "video"
		} else if stringInSlice(download.Extension, []string{".psd"}) ||
			stringInSlice(download.Extension, []string{".nef"}) ||
			stringInSlice(download.Extension, []string{".dng"}) ||
			stringInSlice(download.Extension, []string{".tif"}) ||
			stringInSlice(download.Extension, []string{".tiff"}) {
			contentTypeBase = "image"
		}

		// Check extension
		if sourceConfig.Filters.AllowedExtensions != nil || sourceConfig.Filters.BlockedExtensions != nil {
			shouldAbort := false

			if sourceConfig.Filters.BlockedExtensions != nil {
				if stringInSlice(download.Extension, *sourceConfig.Filters.BlockedExtensions) {
					shouldAbort = true
				}
			}
			if sourceConfig.Filters.AllowedExtensions != nil {
				shouldAbort = shouldAbort || !stringInSlice(download.Extension, *sourceConfig.Filters.AllowedExtensions)
			}

			// Abort
			if shouldAbort {
				if !download.HistoryCmd && !isHtml {
					log.Println(lg("Download", "Skip", color.GreenString, "Unpermitted extension (%s) found at %s",
						download.Extension, download.InputURL))
				}
				return mDownloadStatus(downloadSkippedUnpermittedExtension), 0
			}
		}

		// Check content type. "Other" covers every non-media/text top-level
		// type (for example font/* and model/*), not only application/*.
		contentTypeAllowed := false
		switch contentTypeBase {
		case "image":
			contentTypeAllowed = *sourceConfig.SaveImages
		case "video":
			contentTypeAllowed = *sourceConfig.SaveVideos
		case "audio":
			contentTypeAllowed = *sourceConfig.SaveAudioFiles
		case "text":
			contentTypeAllowed = *sourceConfig.SaveTextFiles && !isHtml
		default:
			contentTypeAllowed = *sourceConfig.SaveOtherFiles && !isHtml
		}
		if !contentTypeAllowed {
			if !download.HistoryCmd && !isHtml {
				log.Println(lg("Download", "Skip", color.GreenString,
					"Unpermitted filetype (%s) found at %s", contentTypeBase, download.InputURL))
			}
			return mDownloadStatus(downloadSkippedUnpermittedType), 0
		}

		// Duplicate Image Filter
		duploEnabled := config.Duplo
		if sourceConfig.Duplo != nil {
			duploEnabled = *sourceConfig.Duplo
		}
		duploThreshold := config.DuploThreshold
		if sourceConfig.DuploThreshold != nil {
			duploThreshold = *sourceConfig.DuploThreshold
		}
		if duploEnabled && contentTypeBase == "image" && download.Extension != ".gif" && download.Extension != ".webp" {
			imageFile, openErr := os.Open(tempDownloadPath)
			if openErr != nil {
				return mDownloadStatus(downloadFailedReadResponse, openErr), 0
			}
			img, _, err := image.Decode(imageFile)
			imageFile.Close()
			if err != nil {
				log.Println(lg("Duplo", "Download", color.HiRedString,
					"Error converting buffer to image for hashing:\t%s", err))
			} else {
				hash, _ := duplo.CreateHash(img)
				isDuplicate := func() bool {
					duploMu.Lock()
					defer duploMu.Unlock()
					if duploCatalog == nil {
						duploCatalog = duplo.New()
					}
					matches := duploCatalog.Query(hash)
					sort.Sort(matches)
					for _, match := range matches {
						if match.Score < duploThreshold {
							log.Println(lg("Duplo", "Download", color.GreenString,
								"Duplicate detected (Score of %f) found at %s", match.Score, download.InputURL))
							return true
						}
					}
					duploCatalog.Add(downloadID, hash)
					duploReserved = true
					return false
				}()
				if isDuplicate {
					return mDownloadStatus(downloadSkippedDetectedDuplicate), 0
				}
			}
		}

		sourceName := "UNKNOWN"
		sourceChannelName := "UNKNOWN"
		if !download.EmojiCmd {
			// Names
			sourceChannel, _ := getChannel(download.Message.ChannelID)
			sourceChannelName = download.Message.ChannelID
			if sourceChannel != nil {
				// Channel Naming
				if sourceChannel.Name != "" {
					sourceChannelName = sourceChannel.Name
				}
				switch sourceChannel.Type {
				case discordgo.ChannelTypeDM:
					sourceName = "Direct Messages"
				case discordgo.ChannelTypeGroupDM:
					sourceName = "Group Messages"
				default:
					// Server Naming
					if sourceChannel.GuildID != "" {
						sourceGuild, _ := getServer(sourceChannel.GuildID)
						if sourceGuild != nil {
							if sourceGuild.Name != "" {
								sourceName = sourceGuild.Name
							} else {
								sourceName = sourceChannel.GuildID
							}
						}
					}
				}
			}

			// Subfolder Division - Format Subfolders
			if sourceConfig.Subfolders != nil {
				subfolders := formattedDownloadSubfolders(sourceConfig, download, contentTypeBase)

				// Subfolder Dividion - Handle Formatted Subfolders
				subpath := ""
				for _, subfolder := range subfolders {
					subpath = subpath + subfolder + string(os.PathSeparator)
					// Create folder
					if err := os.MkdirAll(download.Path+subpath, 0755); err != nil {
						log.Println(lg("Download", "", color.HiRedString,
							"Error while creating subfolder \"%s\": %s", download.Path+subpath, err))
						return mDownloadStatus(downloadFailedCreatingSubfolder, err), 0
					}
				}
				// Format Path
				download.Path = download.Path + subpath // overwrite with new destination path
			}
		}
		completePath := filepath.Clean(filepath.Join(download.Path, download.Filename))
		if !pathIsWithinBase(download.Path, completePath) {
			return mDownloadStatus(downloadFailedInvalidPath,
				fmt.Errorf("download filename escapes destination: %q", download.Filename)), 0
		}
		mediaPath := tempDownloadPath
		savedToDisk := false

		// Check and write under one lock so concurrent messages cannot overwrite
		// the same destination after both observe it as missing.
		downloadWriteMu.Lock()
		if _, statErr := os.Stat(completePath); statErr == nil {
			if *sourceConfig.SavePossibleDuplicates {
				tmpPath := completePath
				i := 1
				for {
					// Append number to name
					candidateName := filenameWithSuffix(filepath.Base(tmpPath), "-"+strconv.Itoa(i), 255)
					completePath = filepath.Join(filepath.Dir(tmpPath), candidateName)
					if _, candidateErr := os.Stat(completePath); os.IsNotExist(candidateErr) {
						break
					} else if candidateErr != nil {
						downloadWriteMu.Unlock()
						return mDownloadStatus(downloadFailedWritingFile, candidateErr), 0
					}
					i = i + 1
				}
				if !download.HistoryCmd && !download.EmojiCmd {
					log.Println(lg("Download", "Skip", color.GreenString,
						"Matching filenames, possible duplicate? Saving \"%s\" as \"%s\" instead",
						tmpPath, completePath))
				}
			} else {
				downloadWriteMu.Unlock()
				if !download.HistoryCmd && !download.EmojiCmd {
					log.Println(lg("Download", "Skip", color.GreenString,
						"Matching filenames, possible duplicate..."))
				}
				return mDownloadStatus(downloadSkippedDuplicate), 0
			}
		} else if !os.IsNotExist(statErr) {
			downloadWriteMu.Unlock()
			return mDownloadStatus(downloadFailedWritingFile, statErr), 0
		}
		download.Filename = filepath.Base(completePath)

		// Write
		if *sourceConfig.Save {
			if err = os.Chmod(tempDownloadPath, 0644); err != nil {
				downloadWriteMu.Unlock()
				return mDownloadStatus(downloadFailedWritingFile, err), 0
			}
			if err = os.Rename(tempDownloadPath, completePath); err != nil {
				downloadWriteMu.Unlock()
				log.Println(lg("Download", "", color.HiRedString,
					"Error while writing file to disk \"%s\": %s", download.InputURL, err))
				return mDownloadStatus(downloadFailedWritingFile, err), 0
			}
			savedToDisk = true
			mediaPath = completePath

			// Change file time
			if err = os.Chtimes(completePath, download.FileTime, download.FileTime); err != nil {
				log.Println(lg("Download", "", color.RedString,
					logPrefix+"Error while changing metadata date \"%s\": %s", download.InputURL, err))
			}

			filesize := "unknown"
			speed := 0.0
			speedlabel := "kB/s"
			fileinfo, err = os.Stat(completePath)
			if err == nil {
				filesize = humanize.Bytes(uint64(fileinfo.Size()))
				speed = float64(fileinfo.Size() / humanize.KByte)
				if fileinfo.Size() >= humanize.MByte {
					speed = float64(fileinfo.Size() / humanize.MByte)
					speedlabel = "MB/s"
				}
			}

			dlColor := color.HiGreenString
			msgTimestamp := ""
			if download.HistoryCmd {
				dlColor = color.HiCyanString
				msgTimestamp = "on " + download.Message.Timestamp.Format("2006/01/02 @ 15:04:05") + " "
			}

			if download.EmojiCmd {
				log.Println(lg("Download", "", dlColor, "Saved emoji/sticker %s", download.Filename))
			} else {
				log.Println(lg("Download", "", dlColor,
					logPrefix+"SAVED %s sent %sin %s\n\t\t\t\t%s",
					strings.ToUpper(contentTypeBase), msgTimestamp,
					color.HiYellowString("\"%s / %s\" (%s, %s)", sourceName, sourceChannelName, download.Message.ChannelID, download.Message.ID),
					color.GreenString("> %s to \"%s\"\t\t%s", domain, completePath,
						color.WhiteString("(%s, %s, %0.1f %s)",
							filesize, timeSinceShort(download.StartTime), speed/time.Since(download.StartTime).Seconds(), speedlabel))))
			}
		} else {
			if !download.EmojiCmd {
				log.Println(lg("Download", "", color.GreenString,
					logPrefix+"Did not save %s sent in %s#%s --- file saving disabled...",
					contentTypeBase, sourceName, sourceChannelName))
			}
		}
		userID := discordUserID()
		if !download.EmojiCmd {
			if download.Message.Author != nil {
				userID = download.Message.Author.ID
			}
		}
		// Store in db
		chID := "0"
		if !download.EmojiCmd {
			chID = download.Message.ChannelID
		}
		err = dbInsertDownload(&downloadItem{
			URL:         download.InputURL,
			Time:        time.Now(),
			Destination: completePath,
			Filename:    filepath.Base(completePath),
			ChannelID:   chID,
			UserID:      userID,
		})
		if err != nil {
			if savedToDisk {
				removeErr := os.Remove(completePath)
				if removeErr != nil && !os.IsNotExist(removeErr) {
					err = fmt.Errorf("%w (also failed to remove untracked file %q: %v)", err, completePath, removeErr)
				}
			}
			downloadWriteMu.Unlock()
			log.Println(lg("Download", "", color.HiRedString, "Error writing to database: %s", err))
			return mDownloadStatus(downloadFailedWritingDatabase, err), 0
		}
		downloadWriteMu.Unlock()
		duploCommitted = true

		// React
		if !download.EmojiCmd {
			shouldReact := shouldReactToDownload(download.HistoryCmd, sourceConfig)
			if download.Message.Author != nil && shouldReact {
				reaction := defaultReact
				if sourceConfig.ReactWhenDownloadedEmoji == nil {
					if download.Message.GuildID != "" {
						guild, err := getServer(download.Message.GuildID)
						if err != nil {
							log.Println(lg("Download", "", color.RedString,
								"Error fetching guild state for emojis from %s: %s",
								download.Message.GuildID, err))
						} else if guild != nil {
							blacklist := []string{}
							if sourceConfig.BlacklistReactEmojis != nil {
								blacklist = *sourceConfig.BlacklistReactEmojis
							}
							reaction = chooseDownloadReaction(guild.Emojis, blacklist, defaultReact)
						}
					} else {
						reaction = defaultReact
					}
				} else {
					reaction = *sourceConfig.ReactWhenDownloadedEmoji
				}
				// Add Reaction
				if hasPerms(download.Message.ChannelID, discordgo.PermissionAddReactions) {
					if session := currentDiscordSession(); session != nil {
						err = session.MessageReactionAdd(download.Message.ChannelID, download.Message.ID, reaction)
					}
					if err != nil {
						log.Println(lg("Download", "", color.RedString,
							"Error adding reaction to message: %s", err))
					}
				} else {
					log.Println(lg("Download", "", color.RedString,
						"Bot does not have permission to add reactions in %s", download.Message.ChannelID))
				}
			}
		}

		// Log Media To Channel(s)
		{
			var logMediaChannels []string
			if sourceConfig.SendFileToChannel != nil {
				if *sourceConfig.SendFileToChannel != "" {
					logMediaChannels = append(logMediaChannels, *sourceConfig.SendFileToChannel)
				}
			}
			if sourceConfig.SendFileToChannels != nil {
				logMediaChannels = append(logMediaChannels, *sourceConfig.SendFileToChannels...)
			}
			for _, logChannel := range logMediaChannels {
				if logChannel != "" {
					if session := currentDiscordSession(); session != nil && hasPerms(logChannel, discordgo.PermissionSendMessages) {
						actualFile := false
						if sourceConfig.SendFileDirectly != nil {
							actualFile = *sourceConfig.SendFileDirectly
						}
						msg := ""
						if sourceConfig.SendFileCaption != nil {
							msg = dataKeys_DiscordMessage(*sourceConfig.SendFileCaption, download.Message)
							msg = dataKeysDownload(msg, sourceConfig, download, false)
						}
						msg = strings.ReplaceAll(msg, "\\r", "\r")
						msg = strings.ReplaceAll(msg, "\\n", "\n")
						// File
						if actualFile {
							mediaFile, openErr := os.Open(mediaPath)
							if openErr != nil {
								log.Println(lg("Download", "", color.HiRedString,
									"File log message failed to open local file:\t%s", openErr))
								continue
							}
							_, err := session.ChannelMessageSendComplex(logChannel,
								&discordgo.MessageSend{
									Content: msg,
									File:    &discordgo.File{Name: download.Filename, Reader: mediaFile},
								},
							)
							mediaFile.Close()
							if err != nil {
								log.Println(lg("Download", "", color.HiRedString,
									"File log message failed to send:\t%s", err))
							}
						} else { // Embed
							embed := &discordgo.MessageEmbed{
								Title: fmt.Sprintf("Downloaded: %s", download.Filename),
								Color: getEmbedColor(logChannel),
								Footer: &discordgo.MessageEmbedFooter{
									IconURL: projectIcon,
									Text:    fmt.Sprintf("%s v%s", projectName, projectVersion),
								},
							}
							if contentTypeBase == "image" {
								embed.Image = &discordgo.MessageEmbedImage{URL: download.InputURL}
							} else if contentTypeBase == "video" {
								embed.Video = &discordgo.MessageEmbedVideo{URL: download.InputURL}
							} else {
								embed.Description = fmt.Sprintf("Unsupported filetype: %s\n%s",
									contentTypeBase, download.InputURL)
							}
							_, err := session.ChannelMessageSendComplex(logChannel,
								&discordgo.MessageSend{
									Content: msg,
									Embed:   embed,
								},
							)
							if err != nil {
								log.Println(lg("Download", "", color.HiRedString,
									"File log message failed to send:\t%s", err))
							}
						}
					}
				}
			}
		}

		// Update Presence
		if !download.HistoryCmd {
			setLastUpdated(time.Now())
			if *sourceConfig.PresenceEnabled {
				go updateDiscordPresence()
			}
		}

		setLastDownload(time.Now())
		if *sourceConfig.Save {
			return mDownloadStatus(downloadSuccess), bytesWritten
		} else {
			return mDownloadStatus(downloadSuccess), 0
		}
	}

	return mDownloadStatus(downloadIgnored), 0
}
