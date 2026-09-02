package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/fatih/color"
)

//#region Events

var messageEvents = newMessageEventDeduper()

var sourceLogMu sync.Mutex

func appendSourceLogLine(path, line string, filterDuplicates, enabled bool) (bool, error) {
	if !enabled {
		return false, nil
	}
	sourceLogMu.Lock()
	defer sourceLogMu.Unlock()
	if filterDuplicates {
		if contents, err := os.ReadFile(path); err == nil && strings.Contains(string(contents), line) {
			return false, nil
		}
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return false, err
	}
	_, writeErr := file.WriteString(line)
	closeErr := file.Close()
	if writeErr != nil {
		return false, writeErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	return true, nil
}

type messageEventDeduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newMessageEventDeduper() *messageEventDeduper {
	return &messageEventDeduper{seen: make(map[string]time.Time)}
}

func (d *messageEventDeduper) first(event, messageID string) bool {
	if messageID == "" {
		return true
	}
	now := time.Now()
	key := event + "\x00" + messageID
	d.mu.Lock()
	defer d.mu.Unlock()
	for seenKey, seenAt := range d.seen {
		if now.Sub(seenAt) > 2*time.Minute {
			delete(d.seen, seenKey)
		}
	}
	if _, exists := d.seen[key]; exists {
		return false
	}
	d.seen[key] = now
	return true
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m == nil {
		return
	}
	if messageEvents.first("create", m.ID) {
		handleMessage(m.Message, nil, false, false)
	}
}

func messageUpdate(s *discordgo.Session, m *discordgo.MessageUpdate) {
	if m == nil {
		return
	}
	if messageEvents.first("update", m.ID) {
		if m.EditedTimestamp != nil {
			handleMessage(m.Message, nil, true, false)
		}
	}
}

func handleMessage(m *discordgo.Message, c *discordgo.Channel, edited bool, history bool) []downloadedItem {
	if m == nil {
		return nil
	}
	if m.Author == nil {
		m.Author = &discordgo.User{}
	}
	shouldBail := false //TODO: this is messy, overlapped purpose with shouldAbort used for filters down below in this func.
	shouldBailReason := ""
	// Ignore own messages unless told not to
	if ownUserID := discordUserID(); ownUserID != "" && m.Author.ID == ownUserID && !config.ScanOwnMessages {
		shouldBail = true
		shouldBailReason = "config.ScanOwnMessages"
	}

	if !history && !edited {
		setLastMessage(time.Now())
	}

	// Admin Channel
	if isAdminChannelRegistered(m.ChannelID) {
		m = fixMessage(m)

		// Log
		sendLabel := fmt.Sprintf("%s in \"%s\"#%s",
			getUserIdentifier(*m.Author),
			getServerLabel(m.GuildID), getChannelLabel(m.ChannelID, nil),
		)
		content := m.Content
		if len(m.Attachments) > 0 {
			content = content + fmt.Sprintf(" (%d attachments)", len(m.Attachments))
		}
		if edited {
			log.Println(lg("Message", "ADMIN CHANNEL", color.CyanString, "Edited [%s]: %s", sendLabel, content))
		} else {
			log.Println(lg("Message", "ADMIN CHANNEL", color.CyanString, "[%s]: %s", sendLabel, content))
		}
	}

	// Registered Channel
	if sourceConfig := getSource(m); sourceConfig != emptySourceConfig {
		// Ignore bots if told to do so
		if m.Author.Bot && *sourceConfig.IgnoreBots {
			shouldBail = true
			shouldBailReason = "config.IgnoreBots"
		}
		// Ignore if told so by config
		if (!history && !*sourceConfig.Enabled) || (edited && !*sourceConfig.ScanEdits) {
			shouldBail = true
			shouldBailReason = "config.ScanEdits"
		}

		// Bail due to basic config rules
		if shouldBail {
			if config.Debug {
				log.Println(lg("Debug", "Message", color.YellowString,
					"%s Ignoring message due to %s...", color.HiMagentaString("(CONFIG)"), shouldBailReason))
			}
			return nil
		}

		m = fixMessage(m)

		// Log
		if config.MessageOutput {
			sendLabel := fmt.Sprintf("%s in \"%s\"#%s",
				getUserIdentifier(*m.Author),
				getServerLabel(m.GuildID), getChannelLabel(m.ChannelID, nil),
			)
			content := m.Content
			if len(m.Attachments) > 0 {
				content += fmt.Sprintf(" \t[%d attachments]", len(m.Attachments))
			}
			content += fmt.Sprintf(" \t<%s>", m.ID)

			if !history || config.MessageOutputHistory {
				addOut := ""
				if history && config.MessageOutputHistory && !m.Timestamp.IsZero() {
					addOut = fmt.Sprintf(" @ %s", discordSnowflakeToTimestamp(m.ID, "2006-01-02 15-04-05"))
				}
				if edited {
					log.Println(lg("Message", "", color.CyanString, "Edited [%s%s]: %s", sendLabel, addOut, content))
				} else {
					log.Println(lg("Message", "", color.CyanString, "[%s%s]: %s", sendLabel, addOut, content))
				}
			}
		}

		// Log Messages to File
		if sourceConfig.LogMessages != nil {
			if sourceConfig.LogMessages.Destination != "" {
				encounteredErrors := false
				savePath := filepath.Clean(sourceConfig.LogMessages.Destination)

				// Subfolder Division - Format Subfolders
				if sourceConfig.LogMessages.Subfolders != nil {
					subfolders := []string{}
					for _, subfolder := range *sourceConfig.LogMessages.Subfolders {
						newSubfolder := dataKeys_DiscordMessage(subfolder, m)

						// Scrub subfolder
						newSubfolder = clearSourceLogField(newSubfolder, *sourceConfig.LogMessages)

						// Do Fallback if a line contains an unparsed key (if fallback exists).
						if strings.Contains(newSubfolder, "{{") && strings.Contains(newSubfolder, "}}") &&
							sourceConfig.LogMessages.SubfoldersFallback != nil {
							subfolders = []string{}
							for _, subfolder2 := range *sourceConfig.LogMessages.SubfoldersFallback {
								newSubfolder2 := dataKeys_DiscordMessage(subfolder2, m)

								// Scrub subfolder
								newSubfolder2 = clearSourceLogField(newSubfolder2, *sourceConfig.LogMessages)

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
						log.Println(lg("LogMessages", "", color.HiRedString, "Invalid log subfolder path: %s", pathErr))
						encounteredErrors = true
					} else if err := os.MkdirAll(savePath, 0755); err != nil {
						log.Println(lg("LogMessages", "", color.HiRedString,
							"Error while creating subfolder \"%s\": %s", savePath, err))
						encounteredErrors = true
					}
				}

				if !encounteredErrors {
					if _, err := os.Stat(savePath); err != nil {
						log.Println(lg("LogMessages", "", color.HiRedString,
							"Save path %s is invalid... %s", savePath, err))
					} else {
						// Format filename
						filename := m.ChannelID + ".txt"
						if sourceConfig.LogMessages.FilenameFormat != nil {
							if *sourceConfig.LogMessages.FilenameFormat != "" {
								filename = dataKeys_DiscordMessage(*sourceConfig.LogMessages.FilenameFormat, m)
								// if extension presumed missing
								if !strings.Contains(filename, ".") {
									filename += ".txt"
								}
							}
						}

						// Scrub filename
						filename = safePathComponent(clearSourceLogField(filename, *sourceConfig.LogMessages))

						// Build path
						logPath, pathErr := safePathUnderBase(savePath, filename)
						if pathErr != nil {
							log.Println(lg("LogMessages", "", color.HiRedString, "Invalid log filename: %s", pathErr))
							return nil
						}

						// Prepend
						prefix := ""
						if sourceConfig.LogMessages.LinePrefix != nil {
							prefix = *sourceConfig.LogMessages.LinePrefix
						}
						prefix = dataKeys_DiscordMessage(prefix, m)

						// Append
						suffix := ""
						if sourceConfig.LogMessages.LineSuffix != nil {
							suffix = *sourceConfig.LogMessages.LineSuffix
						}
						suffix = dataKeys_DiscordMessage(suffix, m)

						// New Line
						var newLine string
						msgContent := m.Content
						if contentFmt, err := m.ContentWithMoreMentionsReplaced(bot); err == nil {
							msgContent = contentFmt
						}
						lineContent := msgContent
						if sourceConfig.LogMessages.LineContent != nil {
							lineContent = *sourceConfig.LogMessages.LineContent
						}
						keys := [][]string{
							{"{{message}}", msgContent},
						}
						for _, key := range keys {
							if strings.Contains(lineContent, key[0]) {
								lineContent = strings.ReplaceAll(lineContent, key[0], key[1])
							}
						}
						newLine += "\n" + prefix + lineContent + suffix

						filterDuplicates := sourceConfig.LogMessages.FilterDuplicates != nil && *sourceConfig.LogMessages.FilterDuplicates
						if _, err := appendSourceLogLine(logPath, newLine, filterDuplicates, true); err != nil {
							log.Println(lg("LogMessages", "", color.RedString, "[sourceConfig.LogMessages] Failed to append log file:\t%s", err))
						}
					}
				}
			}
		}

		// Filters
		if sourceConfig.Filters != nil {
			shouldAbort := false

			if sourceConfig.Filters.AllowedPhrases != nil ||
				sourceConfig.Filters.AllowedUsers != nil ||
				sourceConfig.Filters.AllowedRoles != nil {
				shouldAbort = true
				if config.Debug {
					log.Println(lg("Debug", "Message", color.YellowString,
						"%s Filter will be ignoring by default...",
						color.HiMagentaString("(FILTER)")))
				}
			}

			if sourceConfig.Filters.BlockedPhrases != nil {
				for _, phrase := range *sourceConfig.Filters.BlockedPhrases {
					if strings.Contains(m.Content, phrase) && phrase != "" {
						shouldAbort = true
						if config.Debug {
							log.Println(lg("Debug", "Message", color.YellowString,
								"%s blockedPhrases found \"%s\" in message, planning to abort...",
								color.HiMagentaString("(FILTER)"), phrase))
						}
						break
					}
				}
			}
			if sourceConfig.Filters.AllowedPhrases != nil {
				for _, phrase := range *sourceConfig.Filters.AllowedPhrases {
					if strings.Contains(m.Content, phrase) && phrase != "" {
						shouldAbort = false
						if config.Debug {
							log.Println(lg("Debug", "Message", color.YellowString,
								"%s allowedPhrases found \"%s\" in message, planning to process...",
								color.HiMagentaString("(FILTER)"), phrase))
						}
						break
					}
				}
			}

			if sourceConfig.Filters.BlockedUsers != nil {
				if stringInSlice(m.Author.ID, *sourceConfig.Filters.BlockedUsers) {
					shouldAbort = true
					if config.Debug {
						log.Println(lg("Debug", "Message", color.YellowString,
							"%s blockedUsers caught %s, planning to abort...",
							color.HiMagentaString("(FILTER)"), m.Author.ID))
					}
				}
			}
			if sourceConfig.Filters.AllowedUsers != nil {
				if stringInSlice(m.Author.ID, *sourceConfig.Filters.AllowedUsers) {
					shouldAbort = false
					if config.Debug {
						log.Println(lg("Debug", "Message", color.YellowString,
							"%s allowedUsers caught %s, planning to process...",
							color.HiMagentaString("(FILTER)"), m.Author.ID))
					}
				}
			}

			if sourceConfig.Filters.BlockedRoles != nil {
				member := m.Member
				if member == nil {
					member, _ = bot.GuildMember(m.GuildID, m.Author.ID)
				}
				if member != nil {
					for _, role := range member.Roles {
						if stringInSlice(role, *sourceConfig.Filters.BlockedRoles) {
							shouldAbort = true
							if config.Debug {
								log.Println(lg("Debug", "Message", color.YellowString,
									"%s blockedRoles caught %s, planning to abort...",
									color.HiMagentaString("(FILTER)"), role))
							}
							break
						}
					}
				}
			}
			if sourceConfig.Filters.AllowedRoles != nil {
				member := m.Member
				if member == nil {
					member, _ = bot.GuildMember(m.GuildID, m.Author.ID)
				}
				if member != nil {
					for _, role := range member.Roles {
						if stringInSlice(role, *sourceConfig.Filters.AllowedRoles) {
							shouldAbort = false
							if config.Debug {
								log.Println(lg("Debug", "Message", color.YellowString,
									"%s allowedRoles caught %s, planning to allow...",
									color.HiMagentaString("(FILTER)"), role))
							}
							break
						}
					}
				}
			}

			// Abort
			if shouldAbort {
				if config.Debug {
					log.Println(lg("Debug", "Message", color.YellowString,
						"%s Filter decided to ignore message...",
						color.HiMagentaString("(FILTER)")))
				}
				return nil
			}
		}

		// Delays
		delay := 0
		if history {
			if sourceConfig.DelayHandlingHistory != nil {
				delay = *sourceConfig.DelayHandlingHistory
			}
		} else {
			if sourceConfig.DelayHandling != nil {
				delay = *sourceConfig.DelayHandling
			}
		}
		if delay > 0 {
			if config.Debug {
				log.Println(lg("Debug", "Message", color.YellowString, "Delaying for %d milliseconds...", delay))
			}
			if waitForRuntimeStop(safeConfiguredDuration(delay, time.Millisecond)) {
				return nil
			}
		}

		// Process Collected Links
		var downloadedItems []downloadedItem
		files := getLinksByMessage(m)
		for _, file := range files {
			// Blank link?
			if file.Link == "" {
				continue
			}
			if (*sourceConfig.IgnoreEmojis && strings.HasPrefix(file.Link, "https://cdn.discordapp.com/emojis/")) ||
				(*sourceConfig.IgnoreStickers && strings.HasPrefix(file.Link, "https://media.discordapp.net/stickers/")) {
				continue
			}
			// Filter Checks
			if !downloadLinkContentAllowed(file.Link, sourceConfig.Filters) {
				if config.Debug {
					log.Println(lg("Debug", "Message", color.YellowString,
						"%s Filter decided to ignore link...",
						color.HiMagentaString("(FILTER)")))
				}
				continue
			}
			// Output
			if config.Debug && (!history || config.MessageOutputHistory) {
				log.Println(lg("Debug", "Message", color.HiCyanString, "%s", "FOUND FILE: "+file.Link+fmt.Sprintf(" \t<%s>", m.ID)))
			}
			// Handle Download
			request := downloadRequestStruct{
				InputURL:     file.Link,
				Filename:     file.Filename,
				Path:         sourceConfig.Destination,
				Message:      m,
				Channel:      c,
				FileTime:     file.Time,
				HistoryCmd:   history,
				EmojiCmd:     false,
				StartTime:    time.Now(),
				AttachmentID: file.AttachmentID,
			}
			if !history {
				if !queueRealTimeDownload(request) {
					log.Println(lg("Download", "Queue", color.HiRedString, "Download queue is unavailable; skipping %s", file.Link))
				}
				continue
			}
			status, filesize := runHistoryDownload(request)
			// Await Status
			if status.Status == downloadSuccess {
				domain, _ := getDomain(file.Link)
				downloadedItems = append(downloadedItems, downloadedItem{
					URL:      file.Link,
					Domain:   domain,
					Filesize: filesize,
				})
			}
		}
		return downloadedItems
	}

	return nil
}

//#endregion
