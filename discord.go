package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AvraamMavridis/randomcolor"
	"github.com/aidarkhanov/nanoid/v2"
	"github.com/bwmarrin/discordgo"
	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
	"github.com/teris-io/shortid"
)

// configureDiscordAPI upgrades every eagerly-computed DiscordGo REST root as
// well as the Gateway query version. DiscordGo intentionally exposes these
// variables for endpoint overrides, but changing APIVersion alone would leave
// the derived strings pointing at the package default.
func configureDiscordAPI() {
	discordgo.APIVersion = "10"
	discordgo.EndpointAPI = discordgo.EndpointDiscord + "api/v10/"
	discordgo.EndpointGuilds = discordgo.EndpointAPI + "guilds/"
	discordgo.EndpointChannels = discordgo.EndpointAPI + "channels/"
	discordgo.EndpointUsers = discordgo.EndpointAPI + "users/"
	discordgo.EndpointGateway = discordgo.EndpointAPI + "gateway"
	discordgo.EndpointGatewayBot = discordgo.EndpointGateway + "/bot"
	discordgo.EndpointWebhooks = discordgo.EndpointAPI + "webhooks/"
	discordgo.EndpointStickers = discordgo.EndpointAPI + "stickers/"
	discordgo.EndpointStageInstances = discordgo.EndpointAPI + "stage-instances"
	discordgo.EndpointSKUs = discordgo.EndpointAPI + "skus"
	discordgo.EndpointVoice = discordgo.EndpointAPI + "voice/"
	discordgo.EndpointVoiceRegions = discordgo.EndpointVoice + "regions"
	discordgo.EndpointNitroStickersPacks = discordgo.EndpointAPI + "sticker-packs"
	discordgo.EndpointGuildCreate = discordgo.EndpointAPI + "guilds"
	discordgo.EndpointApplications = discordgo.EndpointAPI + "applications"
	discordgo.EndpointOAuth2 = discordgo.EndpointAPI + "oauth2/"
	discordgo.EndpointOAuth2Applications = discordgo.EndpointOAuth2 + "applications"
	discordgo.EndpointOauth2 = discordgo.EndpointOAuth2
	discordgo.EndpointOauth2Applications = discordgo.EndpointOAuth2Applications
}

const (
	fmtBotSendPerm = "Bot does not have permission to send messages in %s"
)

//#region Getters

func getChannel(channelID string) (*discordgo.Channel, error) {
	session := currentDiscordSession()
	if session == nil {
		return nil, errors.New("Discord session is not initialized")
	}
	if session.State != nil {
		if channel, err := session.State.Channel(channelID); err == nil && channel != nil {
			return channel, nil
		}
	}
	return session.Channel(channelID)
}

func getChannelErr(channelID string) error {
	_, errr := getChannel(channelID)
	return errr
}

func getServer(guildID string) (*discordgo.Guild, error) {
	session := currentDiscordSession()
	if session == nil {
		return nil, errors.New("Discord session is not initialized")
	}
	if session.State != nil {
		if guild, err := session.State.Guild(guildID); err == nil && guild != nil {
			return guild, nil
		}
	}
	return session.Guild(guildID)
}

func getServerErr(guildID string) error {
	_, errr := getServer(guildID)
	return errr
}

//#endregion

//#region Labels

func getServerLabel(serverID string) (displayLabel string) {
	displayLabel = "Discord"
	if serverID == "" {
		return displayLabel
	}
	sourceGuild, _ := getServer(serverID)
	if sourceGuild != nil {
		if sourceGuild.Name != "" {
			displayLabel = sourceGuild.Name
		}
	}
	return displayLabel
}

func getCategoryLabel(channelID string) (displayLabel string) {
	displayLabel = "Category"
	sourceChannel, _ := getChannel(channelID)
	if sourceChannel != nil && sourceChannel.ParentID != "" {
		if sourceParent, err := getChannel(sourceChannel.ParentID); err == nil && sourceParent != nil && sourceParent.Name != "" {
			displayLabel = sourceParent.Name
		}
	}
	return displayLabel
}

func getChannelLabel(channelID string, channelData *discordgo.Channel) (displayLabel string) {
	displayLabel = channelID
	sourceChannel := channelData
	if sourceChannel == nil {
		sourceChannel, _ = getChannel(channelID)
	}
	if sourceChannel != nil {
		if sourceChannel.Name != "" {
			displayLabel = sourceChannel.Name
		} else if sourceChannel.Topic != "" {
			displayLabel = sourceChannel.Topic
		} else {
			switch sourceChannel.Type {
			case discordgo.ChannelTypeDM:
				displayLabel = "DM"
			case discordgo.ChannelTypeGroupDM:
				displayLabel = "Group-DM"
			}
		}
	}
	return displayLabel
}

func getUserIdentifier(usr discordgo.User) string {
	if usr.Discriminator == "0" {
		return "@" + usr.Username
	}
	return fmt.Sprintf("\"%s\"#%s", usr.Username, usr.Discriminator)
}

//#endregion

//#region Time

const (
	discordEpoch = 1420070400000
)

//TODO: Clean these two

func discordTimestampToSnowflake(format string, timestamp string) string {
	var snowflake string = ""
	var err error
	parsed, err := time.ParseInLocation(format, timestamp, time.Local)
	if err == nil {
		milliseconds := parsed.UnixMilli() - discordEpoch
		if milliseconds < 0 || milliseconds > (1<<63-1)>>22 {
			return ""
		}
		snowflake = fmt.Sprint(milliseconds << 22)
	} else {
		log.Println(lg("Main", "", color.HiRedString,
			"Failed to convert timestamp to discord snowflake... Format: '%s', Timestamp: '%s' - Error:\t%s",
			format, timestamp, err))
	}
	return snowflake
}

func discordSnowflakeToTimestamp(snowflake string, format string) string {
	i, err := strconv.ParseInt(snowflake, 10, 64)
	if err != nil {
		return ""
	}
	t := time.Unix(0, ((i>>22)+discordEpoch)*1000000)
	return t.Local().Format(format)
}

//#endregion

//#region Messages

// For command case-insensitivity
func messageToLower(message *discordgo.Message) *discordgo.Message {
	newMessage := *message
	newMessage.Content = strings.ToLower(newMessage.Content)
	return &newMessage
}

func fixMessage(m *discordgo.Message, executeCommands bool) *discordgo.Message {
	// If message content is empty (likely due to userbot/selfbot)
	ubIssue := "Message is corrupted due to endpoint restriction"
	if m.Content == "" && len(m.Attachments) == 0 && len(m.Embeds) == 0 && len(m.StickerItems) == 0 && len(m.MessageSnapshots) == 0 {
		session := currentDiscordSession()
		if session == nil {
			return m
		}
		// Get message history
		mCache, err := session.ChannelMessages(m.ChannelID, 20, "", "", "")
		if err == nil {
			if len(mCache) > 0 {
				for _, mCached := range mCache {
					if mCached == nil {
						continue
					}
					if mCached.ID == m.ID {
						// Fix original message having empty Guild ID
						serverID := m.GuildID
						// Replace message
						m = mCached
						// ^^
						if m.GuildID == "" && serverID != "" {
							m.GuildID = serverID
						}
						// Parse commands
						if executeCommands && botCommands != nil {
							botCommands.FindAndExecute(session, strings.ToLower(config.CommandPrefix), discordUserID(), messageToLower(m))
						}

						break
					}
				}
			} else if config.Debug {
				log.Println(lg("Debug", "fixMessage",
					color.RedString, "%s, and an attempt to get channel messages found nothing...",
					ubIssue))
			}
		} else if config.Debug {
			log.Println(lg("Debug", "fixMessage",
				color.HiRedString, "%s, and an attempt to get channel messages encountered an error:\t%s", ubIssue, err))
		}
	}
	if m.Content == "" && len(m.Attachments) == 0 && len(m.Embeds) == 0 {
		if config.Debug && selfbot {
			log.Println(lg("Debug", "fixMessage",
				color.YellowString, "%s, and attempts to fix seem to have failed...", ubIssue))
		}
	}
	return m
}

//#endregion

func channelDisplay(channelID string) (sourceName string, sourceChannelName string) {
	sourceChannelName = channelID
	sourceName = "UNKNOWN"
	sourceChannel, _ := getChannel(channelID)
	if sourceChannel != nil {
		// Channel Naming
		if sourceChannel.Name != "" {
			sourceChannelName = "#" + sourceChannel.Name // #example
		}
		switch sourceChannel.Type {
		case discordgo.ChannelTypeGuildText:
		case discordgo.ChannelTypeGuildNews:
		case discordgo.ChannelTypeGuildNewsThread:
		case discordgo.ChannelTypeGuildPrivateThread:
		case discordgo.ChannelTypeGuildPublicThread:
			// Server Naming
			if sourceChannel.GuildID != "" {
				sourceGuild, _ := getServer(sourceChannel.GuildID)
				if sourceGuild != nil && sourceGuild.Name != "" {
					sourceName = sourceGuild.Name
				}
			}
			// Category Naming
			if sourceChannel.ParentID != "" {
				sourceParent, _ := getChannel(sourceChannel.ParentID)
				if sourceParent != nil {
					if sourceParent.Name != "" {
						sourceChannelName = sourceParent.Name + " - " + sourceChannelName
					}
				}
			}
		case discordgo.ChannelTypeDM:
			sourceName = "Direct Messages"
		case discordgo.ChannelTypeGroupDM:
			sourceName = "Group Messages"
		}
	}
	return sourceName, sourceChannelName
}

//#region Presence

func dataKeys(input string) string {
	//TODO: Case-insensitive key replacement. -- If no streamlined way to do it, convert to lower to find substring location but replace normally
	if strings.Contains(input, "{{") && strings.Contains(input, "}}") {
		inflateCount := int64(0)
		if config.InflateDownloadCount != nil {
			inflateCount = *config.InflateDownloadCount
		}
		countInt := int64(dbDownloadCount()) + inflateCount
		timeNow := time.Now()
		lastUpdated, _, _ := runtimeTelemetrySnapshot()
		botUsername := ""
		if user := currentDiscordUser(); user != nil {
			botUsername = user.Username
		}
		keys := [][]string{
			{"{{dgVersion}}",
				discordgo.VERSION},
			{"{{ddgVersion}}",
				projectVersion},
			{"{{apiVersion}}",
				discordgo.APIVersion},
			{"{{botUsername}}",
				clearPathIllegalChars(botUsername)},
			{"{{countNoCommas}}",
				fmt.Sprint(countInt)},
			{"{{count}}",
				formatNumber(countInt)},
			{"{{countShort}}",
				formatNumberShort(countInt)},
			{"{{numServers}}",
				fmt.Sprint(discordGuildCount())},
			{"{{numBoundChannels}}",
				fmt.Sprint(getBoundChannelsCount())},
			{"{{numBoundCategories}}",
				fmt.Sprint(getBoundCategoriesCount())},
			{"{{numBoundServers}}",
				fmt.Sprint(getBoundServersCount())},
			{"{{numBoundUsers}}",
				fmt.Sprint(getBoundUsersCount())},
			{"{{numAdminChannels}}",
				fmt.Sprint(len(config.AdminChannels))},
			{"{{numAdmins}}",
				fmt.Sprint(len(config.Admins))},
			//TODO: redo time stuff
			{"{{timeSavedShort}}",
				lastUpdated.Format("3:04pm")},
			{"{{timeSavedShortTZ}}",
				lastUpdated.Format("3:04pm MST")},
			{"{{timeSavedMid}}",
				lastUpdated.Format("3:04pm MST 1/2/2006")},
			{"{{timeSavedLong}}",
				lastUpdated.Format("3:04:05pm MST - January 2, 2006")},
			{"{{timeSavedShort24}}",
				lastUpdated.Format("15:04")},
			{"{{timeSavedShortTZ24}}",
				lastUpdated.Format("15:04 MST")},
			{"{{timeSavedMid24}}",
				lastUpdated.Format("15:04 MST 2/1/2006")},
			{"{{timeSavedLong24}}",
				lastUpdated.Format("15:04:05 MST - 2 January, 2006")},
			{"{{timeNowShort}}",
				timeNow.Format("3:04pm")},
			{"{{timeNowShortTZ}}",
				timeNow.Format("3:04pm MST")},
			{"{{timeNowMid}}",
				timeNow.Format("3:04pm MST 1/2/2006")},
			{"{{timeNowLong}}",
				timeNow.Format("3:04:05pm MST - January 2, 2006")},
			{"{{timeNowShort24}}",
				timeNow.Format("15:04")},
			{"{{timeNowShortTZ24}}",
				timeNow.Format("15:04 MST")},
			{"{{timeNowMid24}}",
				timeNow.Format("15:04 MST 2/1/2006")},
			{"{{timeNowLong24}}",
				timeNow.Format("15:04:05 MST - 2 January, 2006")},
			{"{{uptime}}",
				timeSinceShort(startTime)},
		}
		for _, key := range keys {
			if strings.Contains(input, key[0]) {
				input = strings.ReplaceAll(input, key[0], key[1])
			}
		}
	}
	return input
}

func dataKeysDownload(input string, sourceConfig configurationSource, download downloadRequestStruct, buildingFilename bool) string {
	//TODO: same as dataKeys
	ret := input

	if buildingFilename {
		if sourceConfig == emptySourceConfig {
			return config.FilenameFormat
		}
		ret = config.FilenameFormat
		if sourceConfig.FilenameFormat != nil {
			if *sourceConfig.FilenameFormat != "" {
				ret = *sourceConfig.FilenameFormat
			}
		}
	}

	if strings.Contains(ret, "{{") && strings.Contains(ret, "}}") {

		// Format Filename Date
		filenameDateFormat := config.FilenameDateFormat
		if sourceConfig.FilenameDateFormat != nil {
			if *sourceConfig.FilenameDateFormat != "" {
				filenameDateFormat = *sourceConfig.FilenameDateFormat
			}
		}
		messageTime := download.Message.Timestamp

		shortID, err := shortid.Generate()
		if err != nil && config.Debug {
			log.Println(lg("Debug", "dataKeysDownload", color.HiCyanString, "Error when generating a shortID %s", err))
		}

		nanoID, err := nanoid.New()
		if err != nil && config.Debug {
			log.Println(lg("Debug", "dataKeysDownload", color.HiCyanString, "Error when creating a nanoID %s", err))
		}

		userID := ""
		username := ""
		if download.Message.Author != nil {
			userID = download.Message.Author.ID
			username = download.Message.Author.Username
		}

		channelName := download.Message.ChannelID
		categoryID := download.Message.ChannelID
		categoryName := download.Message.ChannelID
		guildName := download.Message.GuildID

		chinfo, err := getChannel(download.Message.ChannelID)
		if err == nil && chinfo != nil {
			channelName = chinfo.Name
			categoryID = chinfo.ParentID

			if categoryID != "" {
				if catinfo, err := getChannel(categoryID); err == nil && catinfo != nil {
					categoryName = catinfo.Name
				}
			}
		}
		if download.Message.GuildID != "" {
			if guildinfo, err := getServer(download.Message.GuildID); err == nil && guildinfo != nil {
				guildName = guildinfo.Name
			}
		}

		domain := "unknown"
		if parsedURL, err := url.Parse(download.InputURL); err == nil {
			domain = parsedURL.Hostname()
		}

		fileinfo, err := os.Stat(download.Path + download.Filename)
		filesize := "unknown"
		if err == nil {
			filesize = humanize.Bytes(uint64(fileinfo.Size()))
		}

		fmt_msg := download.Message.Content
		if buildingFilename {
			fmt_msg = clearPathIllegalChars(download.Message.Content)
		}
		fmt_url := download.InputURL
		if buildingFilename {
			fmt_url = clearPathIllegalChars(download.InputURL)
		}

		botUsername := ""
		if user := currentDiscordUser(); user != nil {
			botUsername = user.Username
		}
		keys := [][]string{
			{"{{date}}", messageTime.Format(filenameDateFormat)},
			{"{{file}}", download.Filename},
			{"{{fileType}}", download.Extension},
			{"{{fileSize}}", filesize},
			{"{{attachmentID}}", download.AttachmentID},
			{"{{messageID}}", download.Message.ID},
			{"{{userID}}", userID},
			{"{{username}}", username},
			{"{{usernameNoLeadPeriod}}", func() string {
				usernameCleaned := username
				for strings.HasPrefix(usernameCleaned, ".") {
					if len(usernameCleaned) <= 1 {
						break
					} else {
						usernameCleaned = usernameCleaned[1:]
					}
				}
				return usernameCleaned
			}()},
			{"{{channelID}}", download.Message.ChannelID},
			{"{{channelName}}", channelName},
			{"{{categoryID}}", categoryID},
			{"{{categoryName}}", categoryName},
			{"{{serverID}}", download.Message.GuildID},
			{"{{serverName}}", guildName},
			{"{{message}}", fmt_msg},
			{"{{downloadTime}}", timeSinceShort(download.StartTime)},
			{"{{downloadTimeLong}}", timeSince(download.StartTime)},
			{"{{url}}", fmt_url},
			{"{{domain}}", domain},
			{"{{nanoID}}", nanoID},
			{"{{shortID}}", shortID},
			{"{{botUsername}}",
				clearPathIllegalChars(botUsername)},
		}
		for _, key := range keys {
			if strings.Contains(ret, key[0]) {
				ret = strings.ReplaceAll(ret, key[0], key[1])
			}
		}
	}
	return dataKeys(ret)
}

func dataKeys_DiscordMessage(input string, m *discordgo.Message) string {
	ret := input
	if strings.Contains(ret, "{{") && strings.Contains(ret, "}}") && m != nil {
		botUsername := ""
		if user := currentDiscordUser(); user != nil {
			botUsername = user.Username
		}
		// Basic message data
		keys := [][]string{
			{"{{year}}",
				fmt.Sprint(m.Timestamp.Year())},
			{"{{monthNum}}",
				fmt.Sprintf("%02d", m.Timestamp.Month())},
			{"{{dayOfMonth}}",
				fmt.Sprintf("%02d", m.Timestamp.Day())},
			{"{{hour}}",
				fmt.Sprintf("%02d", m.Timestamp.Hour())},
			{"{{minute}}",
				fmt.Sprintf("%02d", m.Timestamp.Minute())},
			{"{{second}}",
				fmt.Sprintf("%02d", m.Timestamp.Second())},
			{"{{timestamp}}", discordSnowflakeToTimestamp(m.ID, "2006-01-02_15-04-05")},
			{"{{timestampYYYYMMDD}}", discordSnowflakeToTimestamp(m.ID, "2006-01-02")},
			{"{{timestampHHMMSS}}", discordSnowflakeToTimestamp(m.ID, "15-04-05")},
			{"{{messageID}}", m.ID},
			{"{{message}}", clearPathIllegalChars(m.Content)},
			{"{{channelID}}", m.ChannelID},
			{"{{botUsername}}",
				clearPathIllegalChars(botUsername)},
		}
		// Author data if present
		if m.Author != nil {
			keys = append(keys, [][]string{
				{"{{userID}}", m.Author.ID},
				{"{{username}}", clearPathIllegalChars(m.Author.Username)},
				{"{{usernameNoLeadPeriod}}", func() string {
					usernameCleaned := clearPathIllegalChars(m.Author.Username)
					for strings.HasPrefix(usernameCleaned, ".") {
						if len(usernameCleaned) <= 1 {
							break
						} else {
							usernameCleaned = usernameCleaned[1:]
						}
					}
					return usernameCleaned
				}()},
				{"{{userDisc}}", m.Author.Discriminator},
			}...)
		}
		// Lookup channel
		var ch *discordgo.Channel
		session := currentDiscordSession()
		if session != nil {
			ch, _ = session.Channel(m.ChannelID)
			if ch == nil && session.State != nil {
				ch, _ = session.State.Channel(m.ChannelID)
			}
		}
		if ch != nil && session != nil {
			keys = append(keys, [][]string{
				{"{{channelName}}", clearPathIllegalChars(ch.Name)},
				{"{{channelTopic}}", clearPathIllegalChars(ch.Topic)},
				{"{{serverID}}", ch.GuildID},
			}...)
			// Lookup server
			var srv *discordgo.Guild
			srv, _ = session.Guild(ch.GuildID)
			if srv == nil && session.State != nil {
				srv, _ = session.State.Guild(ch.GuildID)
			}
			if srv != nil {
				keys = append(keys, [][]string{
					{"{{serverName}}", clearPathIllegalChars(srv.Name)},
				}...)
			}
			// Lookup parent channel
			if ch.ParentID != "" {
				var cat *discordgo.Channel
				cat, _ = session.Channel(ch.ParentID)
				if cat == nil && session.State != nil {
					cat, _ = session.State.Channel(ch.ParentID)
				}
				if cat != nil {
					if cat.Type == discordgo.ChannelTypeGuildCategory {
						keys = append(keys, [][]string{
							{"{{categoryID}}", cat.ID},
							{"{{categoryName}}", clearPathIllegalChars(cat.Name)},
							{"{{forumID}}", ch.ID},                            // no check that this is actually a forum, just accountability so it's not using {{}}
							{"{{forumName}}", clearPathIllegalChars(ch.Name)}, // ^^^
						}...)
					} else {
						keys = append(keys, [][]string{
							{"{{threadID}}", ch.ID},
							{"{{threadName}}", clearPathIllegalChars(ch.Name)},
							{"{{threadTopic}}", clearPathIllegalChars(ch.Topic)},
							{"{{forumID}}", cat.ID},
							{"{{forumName}}", clearPathIllegalChars(cat.Name)},
						}...)
						// Parent Category
						if cat.ParentID != "" {
							var cat2 *discordgo.Channel
							if session.State != nil {
								cat2, _ = session.State.Channel(cat.ParentID)
							}
							if cat2 == nil {
								cat2, _ = session.Channel(cat.ParentID)
							}
							if cat2 != nil {
								keys = append(keys, [][]string{
									{"{{categoryID}}", cat2.ID},
									{"{{categoryName}}", clearPathIllegalChars(cat2.Name)},
								}...)
							}
						}
					}
				}
			}
		}
		for _, key := range keys {
			if strings.Contains(ret, key[0]) {
				ret = strings.ReplaceAll(ret, key[0], key[1])
			}
		}
	}

	// Cleanup
	ret = strings.ReplaceAll(ret, "{{channelName}}", "DM")
	ret = strings.ReplaceAll(ret, "{{channelTopic}}", "DM")
	ret = strings.ReplaceAll(ret, "{{serverName}}", "DM")
	ret = strings.ReplaceAll(ret, "{{categoryID}}", "Uncategorized")
	ret = strings.ReplaceAll(ret, "{{categoryName}}", "Uncategorized")
	ret = strings.ReplaceAll(ret, "{{forumID}}", "NOT_FORUM")
	ret = strings.ReplaceAll(ret, "{{forumName}}", "NOT_FORUM")
	ret = strings.ReplaceAll(ret, "{{threadID}}", "NOT_THREAD")
	ret = strings.ReplaceAll(ret, "{{threadName}}", "NOT_THREAD")
	ret = strings.ReplaceAll(ret, "{{threadTopic}}", "NOT_THREAD")

	return ret
}

func dataKeys_DownloadStatus(input string, status downloadStatusStruct, download downloadRequestStruct) string {
	ret := input
	if strings.Contains(ret, "{{") && strings.Contains(ret, "}}") {
		// Basic message data
		keys := [][]string{
			{"{{downloadStatus}}", getDownloadStatusShort(status.Status)},
			{"{{downloadStatusLong}}", getDownloadStatus(status.Status)},
			{"{{downloadFilename}}", download.Filename},
			{"{{downloadExt}}", download.Extension},
			{"{{downloadPath}}", download.Path},
		}
		for _, key := range keys {
			if strings.Contains(ret, key[0]) {
				ret = strings.ReplaceAll(ret, key[0], key[1])
			}
		}
	}
	return ret
}

func updateDiscordPresence() {
	if !botReady.Load() {
		return
	}
	session := currentDiscordSession()
	if session == nil {
		return
	}
	updated, _, _ := runtimeTelemetrySnapshot()
	if config.PresenceEnabled {
		// Vars
		countInt := int64(dbDownloadCount()) + *config.InflateDownloadCount
		count := formatNumber(countInt)
		countShort := formatNumberShort(countInt)
		timeShort := updated.Format("3:04pm")
		timeLong := updated.Format("3:04:05pm MST - January 2, 2006")

		// Defaults
		status := fmt.Sprintf("%s - %s files", timeShort, countShort)
		statusDetails := timeLong
		statusState := fmt.Sprintf("%s files total", count)

		// Overwrite Presence
		if config.PresenceLabel != nil {
			status = *config.PresenceLabel
			if status != "" {
				status = dataKeys(status)
			}
		}
		// Overwrite Details
		if config.PresenceDetails != nil {
			statusDetails = *config.PresenceDetails
			if statusDetails != "" {
				statusDetails = dataKeys(statusDetails)
			}
		}
		// Overwrite State
		if config.PresenceState != nil {
			statusState = *config.PresenceState
			if statusState != "" {
				statusState = dataKeys(statusState)
			}
		}

		// Update
		session.UpdateStatusComplex(discordgo.UpdateStatusData{
			Activities: []*discordgo.Activity{{
				Name:    status,
				Type:    config.PresenceType,
				Details: statusDetails, // Only visible if real user
				State:   statusState,
			}},
			Status: config.PresenceStatus,
		})
	} else if config.PresenceStatus != string(discordgo.StatusOnline) {
		session.UpdateStatusComplex(discordgo.UpdateStatusData{
			Status: config.PresenceStatus,
		})
	}
}

//#endregion

//#region Embeds

func getEmbedColor(channelID string) int {
	var err error
	var color *string
	var channelInfo *discordgo.Channel
	session := currentDiscordSession()
	userID := discordUserID()

	// Assign Defined Color
	if config.EmbedColor != nil {
		if *config.EmbedColor != "" {
			color = config.EmbedColor
		}
	}
	// Overwrite with Defined Color for Channel
	/*var msg *discordgo.Message
	msg.ChannelID = channelID
	if channelRegistered(msg) {
		sourceConfig := getSource(channelID)
		if sourceConfig.OverwriteEmbedColor != nil {
			if *sourceConfig.OverwriteEmbedColor != "" {
				color = sourceConfig.OverwriteEmbedColor
			}
		}
	}*/

	// Use Defined Color
	if color != nil {
		// Defined as Role, fetch role color
		if *color == "role" || *color == "user" {
			if session != nil && session.State != nil && userID != "" {
				if botColor := session.State.UserColor(userID, channelID); botColor != 0 {
					return botColor
				}
			}
			goto color_random
		}
		// Defined as Random, jump below (not preferred method but seems to work flawlessly)
		if *color == "random" || *color == "rand" {
			goto color_random
		}

		var colorString string = *color

		// Input is Hex
		colorString = strings.ReplaceAll(colorString, "#", "")
		if convertedHex, err := strconv.ParseUint(colorString, 16, 64); err == nil {
			return int(convertedHex)
		}

		// Input is Int
		if convertedInt, err := strconv.Atoi(colorString); err == nil {
			return convertedInt
		}

		// Definition is invalid since hasn't returned, so defaults to below...
	}

	// User color
	if session == nil || session.State == nil || userID == "" {
		goto color_random
	}
	channelInfo, err = session.State.Channel(channelID)
	if err != nil {
		channelInfo, err = session.Channel(channelID)
	}
	if err == nil && channelInfo != nil {
		if channelInfo.Type != discordgo.ChannelTypeDM && channelInfo.Type != discordgo.ChannelTypeGroupDM {
			if botColor := session.State.UserColor(userID, channelID); botColor != 0 {
				return botColor
			}
		}
	}

	// Random color
color_random:
	var randomColor string = randomcolor.GetRandomColorInHex()
	if convertedRandom, err := strconv.ParseUint(strings.ReplaceAll(randomColor, "#", ""), 16, 64); err == nil {
		return int(convertedRandom)
	}

	return 16777215 // white
}

// Shortcut function for quickly constructing a styled embed with Title & Description
func buildEmbed(channelID string, title string, description string) *discordgo.MessageEmbed {
	return &discordgo.MessageEmbed{
		Title:       title,
		Description: description,
		Color:       getEmbedColor(channelID),
		Footer: &discordgo.MessageEmbedFooter{
			IconURL: projectIcon,
			Text:    fmt.Sprintf("%s v%s", projectName, projectVersion),
		},
	}
}

// Shortcut function for quickly replying a styled embed with Title & Description
func replyEmbed(m *discordgo.Message, title string, description string) (*discordgo.Message, error) {
	session := currentDiscordSession()
	if m != nil {
		if session != nil && hasPerms(m.ChannelID, discordgo.PermissionSendMessages) {
			mention := ""
			if m.Author != nil && m.Author.ID != "" {
				mention = m.Author.Mention()
			}
			if !config.CommandTagging { // Erase mention if tagging disabled
				mention = ""
			}
			if selfbot {
				if mention != "" { // Add space if mentioning
					mention += " "
				}
				return session.ChannelMessageSend(m.ChannelID, fmt.Sprintf("%s**%s**\n\n%s", mention, title, description))
			} else {
				return session.ChannelMessageSendComplex(m.ChannelID,
					&discordgo.MessageSend{
						Content: mention,
						Embed:   buildEmbed(m.ChannelID, title, description),
					},
				)
			}
		}
		log.Println(lg("Discord", "replyEmbed", color.HiRedString, fmtBotSendPerm, m.ChannelID))
	}
	return nil, nil
}

//#endregion

//#region Send Status Message

type sendStatusType int

const (
	sendStatusStartup sendStatusType = iota
	sendStatusReconnect
	sendStatusExit
	sendStatusSettings
)

func sendStatusLabel(status sendStatusType) string {
	switch status {
	case sendStatusStartup:
		return "has launched"
	case sendStatusReconnect:
		return "has reconnected"
	case sendStatusExit:
		return "is exiting"
	case sendStatusSettings:
		return "updated settings"
	}
	return "is confused"
}

func sendStatusMessage(status sendStatusType) {
	session := currentDiscordSession()
	if session == nil {
		return
	}
	for _, adminChannel := range config.AdminChannels {
		if *adminChannel.LogStatus {
			var message string
			var label string
			var emoji string

			//TODO: CLEAN
			if status == sendStatusStartup || status == sendStatusReconnect {
				label = "startup"
				emoji = "🟩"
				if status == sendStatusReconnect {
					emoji = "🟧"
				}
				serverCount := discordGuildCount()
				message += fmt.Sprintf("%s %s and connected to %d server%s...\n", projectLabel, sendStatusLabel(status), serverCount, pluralS(serverCount))
				message += fmt.Sprintf("\n• Uptime is %s", uptime())
				message += fmt.Sprintf("\n• %s total downloads", formatNumber(int64(dbDownloadCount())))
				message += fmt.Sprintf("\n• Bound to %d channel%s, %d categories, %d server%s, %d user%s",
					getBoundChannelsCount(), pluralS(getBoundChannelsCount()),
					getBoundCategoriesCount(),
					getBoundServersCount(), pluralS(getBoundServersCount()),
					getBoundUsersCount(), pluralS(getBoundUsersCount()),
				)
				if config.All != nil {
					message += "\n• **ALL MODE ENABLED -** Bot will use all available channels"
				}
				allChannels := getAllRegisteredChannels()
				message += fmt.Sprintf("\n• ***Listening to %s channel%s...***\n", formatNumber(int64(len(allChannels))), pluralS(len(allChannels)))
				message += fmt.Sprintf("\n_%s_", versions(true))
			} else if status == sendStatusExit {
				label = "exit"
				emoji = "🟥"
				message += fmt.Sprintf("%s %s...\n", projectLabel, sendStatusLabel(status))
				message += fmt.Sprintf("\n• Uptime was %s", uptime())
				message += fmt.Sprintf("\n• %s total downloads", formatNumber(int64(dbDownloadCount())))
				message += fmt.Sprintf("\n• Bound to %d channel%s, %d categories, %d server%s, %d user%s",
					getBoundChannelsCount(), pluralS(getBoundChannelsCount()),
					getBoundCategoriesCount(),
					getBoundServersCount(), pluralS(getBoundServersCount()),
					getBoundUsersCount(), pluralS(getBoundUsersCount()),
				)
			} else if status == sendStatusSettings {
				label = "settings"
				emoji = "🟨"
				message += fmt.Sprintf("%s %s...\n", projectLabel, sendStatusLabel(status))
				message += fmt.Sprintf("\n• Bound to %d channel%s, %d categories, %d server%s, %d user%s",
					getBoundChannelsCount(), pluralS(getBoundChannelsCount()),
					getBoundCategoriesCount(),
					getBoundServersCount(), pluralS(getBoundServersCount()),
					getBoundUsersCount(), pluralS(getBoundUsersCount()),
				)
			}
			// Send
			if config.Debug {
				log.Println(lg("Debug", "Bot Status", color.YellowString, "Sending log for %s to admin channel: %s",
					strings.ToUpper(label), getChannelLabel(adminChannel.ChannelID, nil)))
			}
			if hasPerms(adminChannel.ChannelID, discordgo.PermissionEmbedLinks) && !selfbot {
				session.ChannelMessageSendEmbed(adminChannel.ChannelID,
					buildEmbed(adminChannel.ChannelID, emoji+" Log — Status", message))
			} else if hasPerms(adminChannel.ChannelID, discordgo.PermissionSendMessages) {
				session.ChannelMessageSend(adminChannel.ChannelID, message)
			} else {
				log.Println(lg("Debug", "Bot Status", color.HiRedString, "Perms checks failed for sending %s status log to %s",
					strings.ToUpper(label), adminChannel.ChannelID))
			}
		}
	}
}

func sendErrorMessage(err string) {
	session := currentDiscordSession()
	if session == nil {
		return
	}
	for _, adminChannel := range config.AdminChannels {
		if *adminChannel.LogErrors {
			// Send
			if hasPerms(adminChannel.ChannelID, discordgo.PermissionEmbedLinks) && !selfbot { // not confident this is the right permission
				if config.Debug {
					log.Println(lg("Debug", "sendErrorMessage", color.HiCyanString, "Sending embed log for error to %s",
						adminChannel.ChannelID))
				}
				session.ChannelMessageSendEmbed(adminChannel.ChannelID, buildEmbed(adminChannel.ChannelID, "Log — Error", err))
			} else if hasPerms(adminChannel.ChannelID, discordgo.PermissionSendMessages) {
				if config.Debug {
					log.Println(lg("Debug", "sendErrorMessage", color.HiCyanString, "Sending embed log for error to %s",
						adminChannel.ChannelID))
				}
				session.ChannelMessageSend(adminChannel.ChannelID, err)
			} else {
				log.Println(lg("Debug", "sendErrorMessage", color.HiRedString, "Perms checks failed for sending error log to %s",
					adminChannel.ChannelID))
			}
		}
	}
}

//#endregion

//#region Permissions

func hasPerms(channelID string, permission int64) bool {
	if channelID == "" {
		return false
	}
	session := currentDiscordSession()
	if session == nil {
		return false
	}
	if selfbot {
		return true
	}

	var sourceChannel *discordgo.Channel
	var err error
	if session.State != nil {
		sourceChannel, err = session.State.Channel(channelID)
	}
	if sourceChannel == nil {
		sourceChannel, err = session.Channel(channelID)
	}
	if sourceChannel != nil && err == nil {
		switch sourceChannel.Type {
		case discordgo.ChannelTypeDM:
			return true
		case discordgo.ChannelTypeGroupDM:
			return true
		default:
			userID := discordUserID()
			if userID == "" {
				return false
			}
			perms, err := session.UserChannelPermissions(userID, channelID)
			if err == nil {
				return perms&permission == permission
			}
			log.Println(lg("Debug", "hasPerms", color.HiRedString,
				"Failed to check permissions (%d) for %s:\t%s", permission, channelID, err))
		}
	}
	return false
}

//#endregion

//#region Download Emojis & Stickers

func downloadDiscordEmojis() {

	dataKeysEmoji := func(emoji discordgo.Emoji, serverID string) string {
		ret := config.EmojisFilenameFormat
		keys := [][]string{
			{"{{ID}}", emoji.ID},
			{"{{name}}", emoji.Name},
		}
		for _, key := range keys {
			if strings.Contains(ret, key[0]) {
				ret = strings.ReplaceAll(ret, key[0], key[1])
			}
		}
		return ret
	}

	if config.EmojisServers != nil {
		session := currentDiscordSession()
		if session == nil {
			log.Println(lg("Discord", "Emojis", color.HiRedString, "Discord session is unavailable"))
			return
		}
		// Handle destination
		destination := "emojis"
		if config.EmojisDestination != nil {
			destination = *config.EmojisDestination
		}
		if err := os.MkdirAll(destination, 0755); err != nil {
			log.Println(lg("Discord", "Emojis", color.HiRedString, "Error while creating destination folder \"%s\": %s", destination, err))
			return
		}
		// Start
		log.Println(lg("Discord", "Emojis", color.MagentaString, "Starting emoji downloads..."))
		for _, serverID := range *config.EmojisServers {
			if runtimeIsStopping() {
				return
			}
			emojis, err := session.GuildEmojis(serverID)
			if err != nil {
				log.Println(lg("Discord", "Emojis", color.HiRedString, "Error fetching emojis from %s... %s", serverID, err))
			} else {
				guildName := "UNKNOWN"
				guild, err := session.Guild(serverID)
				if err == nil && guild != nil {
					guildName = guild.Name
				}
				subfolder, pathErr := safePathUnderBase(destination, clearPathIllegalChars(guildName))
				if pathErr != nil {
					log.Println(lg("Discord", "Emojis", color.HiRedString, "Invalid emoji subfolder: %s", pathErr))
					continue
				}
				if err = os.MkdirAll(subfolder, 0755); err != nil {
					log.Println(lg("Discord", "Emojis", color.HiRedString, "Error while creating subfolder \"%s\": %s", subfolder, err))
					continue
				}

				countDownloaded := 0
				countSkipped := 0
				countFailed := 0
				for _, emoji := range emojis {
					if runtimeIsStopping() {
						return
					}
					if emoji == nil {
						continue
					}
					url := "https://cdn.discordapp.com/emojis/" + emoji.ID

					status, _ := runHistoryDownload(downloadRequestStruct{
						InputURL:   url,
						Filename:   dataKeysEmoji(*emoji, serverID),
						Path:       subfolder,
						Message:    nil,
						Channel:    nil,
						FileTime:   time.Now(),
						HistoryCmd: false,
						EmojiCmd:   true,
						StartTime:  time.Now(),
					})

					if status.Status == downloadSuccess {
						countDownloaded++
					} else if status.Status == downloadSkippedDuplicate {
						countSkipped++
					} else {
						countFailed++
						log.Println(lg("Discord", "Emojis", color.HiRedString,
							"Failed to download emoji \"%s\": \t[%d - %s] %v",
							url, status.Status, getDownloadStatus(status.Status), status.Error))
					}
				}

				// Log
				destinationOut := destination
				abs, err := filepath.Abs(destination)
				if err == nil {
					destinationOut = abs
				}
				log.Println(lg("Discord", "Emojis", color.HiMagentaString, "%s",
					fmt.Sprintf("%d emojis downloaded, %d skipped, %d failed - Destination: %s",
						countDownloaded, countSkipped, countFailed, destinationOut,
					)))
			}
		}
	}

}

func downloadDiscordStickers() {

	dataKeysSticker := func(sticker discordgo.Sticker) string {
		ret := config.StickersFilenameFormat
		keys := [][]string{
			{"{{ID}}", sticker.ID},
			{"{{name}}", sticker.Name},
		}
		for _, key := range keys {
			if strings.Contains(ret, key[0]) {
				ret = strings.ReplaceAll(ret, key[0], key[1])
			}
		}
		return ret
	}

	if config.StickersServers != nil {
		session := currentDiscordSession()
		if session == nil {
			log.Println(lg("Discord", "Stickers", color.HiRedString, "Discord session is unavailable"))
			return
		}
		// Handle destination
		destination := "stickers"
		if config.StickersDestination != nil {
			destination = *config.StickersDestination
		}
		if err := os.MkdirAll(destination, 0755); err != nil {
			log.Println(lg("Discord", "Stickers", color.HiRedString, "Error while creating destination folder \"%s\": %s", destination, err))
			return
		}
		log.Println(lg("Discord", "Stickers", color.MagentaString, "Starting sticker downloads..."))
		for _, serverID := range *config.StickersServers {
			if runtimeIsStopping() {
				return
			}
			guildName := "UNKNOWN"
			guild, err := session.Guild(serverID)
			if err != nil || guild == nil {
				log.Println(lg("Discord", "Stickers", color.HiRedString, "Error fetching server %s... %s", serverID, err))
			} else {
				guildName = guild.Name
				stickerEndpoint := discordgo.EndpointGuildStickers(serverID)
				stickerData, listErr := session.RequestWithBucketID("GET", stickerEndpoint, nil, stickerEndpoint)
				if listErr != nil {
					log.Println(lg("Discord", "Stickers", color.HiRedString, "Error listing stickers from %s... %s", serverID, listErr))
					continue
				}
				var stickers []*discordgo.Sticker
				if err := json.Unmarshal(stickerData, &stickers); err != nil {
					log.Println(lg("Discord", "Stickers", color.HiRedString, "Invalid sticker list from %s... %s", serverID, err))
					continue
				}
				subfolder, pathErr := safePathUnderBase(destination, clearPathIllegalChars(guildName))
				if pathErr != nil {
					log.Println(lg("Discord", "Stickers", color.HiRedString, "Invalid sticker subfolder: %s", pathErr))
					continue
				}
				if err = os.MkdirAll(subfolder, 0755); err != nil {
					log.Println(lg("Discord", "Emojis", color.HiRedString, "Error while creating subfolder \"%s\": %s", subfolder, err))
					continue
				}

				countDownloaded := 0
				countSkipped := 0
				countFailed := 0
				for _, sticker := range stickers {
					if runtimeIsStopping() {
						return
					}
					if sticker == nil {
						continue
					}
					url := "https://media.discordapp.net/stickers/" + sticker.ID

					status, _ := runHistoryDownload(downloadRequestStruct{
						InputURL:   url,
						Filename:   dataKeysSticker(*sticker),
						Path:       subfolder,
						Message:    nil,
						Channel:    nil,
						FileTime:   time.Now(),
						HistoryCmd: false,
						EmojiCmd:   true,
						StartTime:  time.Now(),
					})

					if status.Status == downloadSuccess {
						countDownloaded++
					} else if status.Status == downloadSkippedDuplicate {
						countSkipped++
					} else {
						countFailed++
						log.Println(lg("Discord", "Stickers", color.HiRedString,
							"Failed to download sticker \"%s\": \t[%d - %s] %v",
							url, status.Status, getDownloadStatus(status.Status), status.Error))
					}
				}

				// Log
				destinationOut := destination
				abs, err := filepath.Abs(destination)
				if err == nil {
					destinationOut = abs
				}
				log.Println(lg("Discord", "Stickers", color.HiMagentaString, "%s",
					fmt.Sprintf("%d stickers downloaded, %d skipped, %d failed - Destination: %s",
						countDownloaded, countSkipped, countFailed, destinationOut,
					)))
			}
		}
	}

}

//#endregion

//#region BOT LOGIN SEQUENCE

func configureDiscordSessionForGateway(session *discordgo.Session) error {
	if session == nil {
		return errors.New("discord session was not initialized")
	}
	if session.Client == nil || session.State == nil {
		return errors.New("discord session is missing its HTTP client or state cache")
	}

	// These options must be installed before Open. Ready and the first gateway
	// events may arrive during the handshake; changing cache tracking afterwards
	// can leave the initial state incomplete until a later event repairs it.
	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions |
		discordgo.IntentsMessageContent
	session.ShouldReconnectOnError = true
	session.Client.Timeout = safeConfiguredDuration(config.DiscordTimeout, time.Second)
	session.StateEnabled = true
	session.State.MaxMessageCount = 100000
	session.State.TrackChannels = true
	session.State.TrackThreads = true
	session.State.TrackMembers = true
	session.State.TrackThreadMembers = true
	return nil
}

func registerDiscordHandlers(session *discordgo.Session) error {
	if session == nil {
		return errors.New("discord session was not initialized")
	}
	setDiscordSession(session)
	botCommands = handleCommands()
	session.AddHandler(messageCreate)
	session.AddHandler(messageUpdate)
	return nil
}

func botLoadDiscord() {
	var err error
	botReady.Store(false)
	setDiscordUser(nil)

	// Discord Login
	connectBot := func(session *discordgo.Session) error {
		if err := configureDiscordSessionForGateway(session); err != nil {
			return err
		}

		// Connect Bot
		session.LogLevel = -1 // to ignore noisy websocket errors during login
		openErr := session.Open()
		session.LogLevel = config.DiscordLogLevel // reset
		if openErr != nil && !strings.Contains(strings.ToLower(openErr.Error()), "web socket already opened") {
			return openErr
		}

		user, userErr := session.User("@me")
		if userErr != nil {
			session.State.RLock()
			user = session.State.User
			session.State.RUnlock()
		}
		if user == nil {
			if userErr != nil {
				return userErr
			}
			return fmt.Errorf("Discord returned no current user")
		}
		setDiscordUser(user)
		return nil
	}

	discord_login_count := 0
do_discord_login:
	discord_login_count++
	if discord_login_count > 1 {
		if session := currentDiscordSession(); session != nil {
			_ = session.Close()
			setDiscordSession(nil)
		}
		setDiscordUser(nil)
		time.Sleep(3 * time.Second)
	}

	if config.Credentials.Token != "" && config.Credentials.Token != placeholderToken {
		// Login via token. DiscordGo requires a `Bot ` prefix for bot tokens;
		// the old code tried the raw token first, which makes valid bot tokens
		// fail before it ever reached the prefixed retry.
		log.Println(lg("Discord", "", color.GreenString, "Connecting to Discord via token..."))
		rawToken := strings.TrimSpace(config.Credentials.Token)
		botToken := rawToken
		if !strings.HasPrefix(strings.ToLower(botToken), "bot ") {
			botToken = "Bot " + botToken
		}
		var session *discordgo.Session
		session, err = discordgo.New(botToken)
		if err == nil {
			err = registerDiscordHandlers(session)
		}
		if err == nil {
			err = connectBot(session)
		}
		// Keep a compatibility fallback for legacy user tokens, but do not hide
		// the policy warning below. User-account automation is not supported by
		// Discord and may stop working at any time.
		if err != nil && !strings.HasPrefix(strings.ToLower(rawToken), "bot ") {
			if session != nil {
				_ = session.Close()
			}
			session, err = discordgo.New(rawToken)
			if err == nil {
				err = registerDiscordHandlers(session)
			}
			if err == nil {
				err = connectBot(session)
			}
		}

	} else if (config.Credentials.Email != "" && config.Credentials.Email != placeholderEmail) ||
		(config.Credentials.Password != "" && config.Credentials.Password != placeholderPassword) {
		// Discord API v10 and current DiscordGo releases intentionally do not
		// support user-account email/password automation. Keep the fields only so
		// old configuration files can be migrated without silently losing data.
		err = fmt.Errorf("email/password Discord login is no longer supported; configure a bot token")
	} else {
		if discord_login_count > 5 {
			log.Println(lg("Discord", "", color.HiRedString, "No valid credentials for Discord..."))
			properExit()
		} else {
			goto do_discord_login
		}
	}
	if err != nil {
		if discord_login_count > 5 {
			log.Println(lg("Discord", "", color.HiRedString, "Error logging in: %s", err))
			properExit()
		} else {
			goto do_discord_login
		}
	}

	// Fetch Bot's User Info (connectBot already populated it when possible).
	user := currentDiscordUser()
	if user == nil {
		if discord_login_count > 5 {
			log.Println(lg("Discord", "", color.HiRedString, "No error encountered obtaining user details, but it's empty..."))
			properExit()
		} else {
			goto do_discord_login
		}
	} else {
		botReady.Store(true)
		log.Println(lg("Discord", "", color.HiGreenString, "Logged into %s", getUserIdentifier(*user)))
		if user.Bot {
			log.Println(lg("Discord", "Info", color.HiMagentaString, "GENUINE DISCORD BOT APPLICATION"))
			log.Println(lg("Discord", "Info", color.MagentaString, "~ This is the safest way to use this bot."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~ INTENTS: Enable Message Content Intent in the Discord Developer Portal."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~ PRESENCE: Details don't work. Only activity and status."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~ VISIBILITY: You can only see servers you have added the bot to, which requires you to be an admin or have an admin invite the bot."))
		} else {
			log.Println(lg("Discord", "Info", color.HiYellowString, "!!! USER ACCOUNT / SELF-BOT !!!"))
			log.Println(lg("Discord", "Info", color.HiMagentaString, "~ WARNING: Discord does NOT ALLOW automated user accounts (aka Self-Bots)."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~~~ By using this bot application with a user account, you potentially risk account termination."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~~~ See the GitHub page for link to Discord's official statement."))
			log.Println(lg("Discord", "Info", color.MagentaString, "~~~ IF YOU WISH TO AVOID THIS, USE A BOT APPLICATION IF POSSIBLE."))
			log.Println(lg("Discord", "Info", color.HiMagentaString, "~ DISCORD API BUGS MAY OCCUR - KNOWN ISSUES:"))
			log.Println(lg("Discord", "Info", color.MagentaString, "~~~ Can't see active threads, only archived threads."))
			log.Println(lg("Discord", "Info", color.HiMagentaString, "~ VISIBILITY: You can download from any channels/servers this account has access to."))
		}
	}
	selfbot = user.Email != "" // is selfbot

	// Start Presence
	setLastUpdated(time.Now())
	go updateDiscordPresence()

	//(SV) Source Validation
	var invalidAdminChannels []string
	var invalidServers []string
	var invalidCategories []string
	var invalidChannels []string
	var missingPermsAdminChannels [][]string
	var missingPermsCategories [][]string
	var missingPermsChannels [][]string
	log.Println(lg("Discord", "Validation", color.GreenString, "Validating your configured Discord sources..."))

	validateSource := func(checkFunc func(string) error, target string, label string, invalidStack *[]string) bool {
		if err := checkFunc(target); err != nil {
			*invalidStack = append(*invalidStack, target)
			log.Println(lg("Discord", "Validation", color.HiRedString,
				"Bot cannot access %s %s...\t%s", label, target, err))
			return false
		}
		return true
	}
	checkChannelPerm := func(perm int64, permName string, target string, label string, invalidStack *[][]string) {
		if perms, err := bot.State.UserChannelPermissions(botUser.ID, target); err == nil {
			if perms&perm == 0 { // lacks permission
				*invalidStack = append(*invalidStack, []string{target, permName})
				log.Println(lg("Discord", "Validation", color.HiRedString,
					"%s %s / %s - Lacks <%s>...", strings.ToUpper(label), target, getChannelLabel(target, nil), permName))
			}
		} else if config.Debug {
			log.Println(lg("Discord", "Validation", color.HiRedString,
				"Encountered error checking Discord permission <%s> in %s %s / %s...\t%s",
				permName, label, target, getChannelLabel(target, nil), err))
		}
	}

	//(SV) Check Admin Channels
	if config.AdminChannels != nil {
		for _, adminChannel := range config.AdminChannels {
			if adminChannel.ChannelIDs != nil {
				for _, subchannel := range *adminChannel.ChannelIDs {
					if validateSource(getChannelErr, subchannel, "admin subchannel", &invalidAdminChannels) {
						checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
							subchannel, "admin subchannel", &missingPermsAdminChannels)
						checkChannelPerm(discordgo.PermissionSendMessages, "PermissionSendMessages",
							subchannel, "admin subchannel", &missingPermsAdminChannels)
					}
				}
			} else {
				if validateSource(getChannelErr, adminChannel.ChannelID, "admin channel", &invalidAdminChannels) {
					checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
						adminChannel.ChannelID, "admin channel", &missingPermsAdminChannels)
					checkChannelPerm(discordgo.PermissionSendMessages, "PermissionSendMessages",
						adminChannel.ChannelID, "admin channel", &missingPermsAdminChannels)
				}
			}
		}
	}
	//(SV) Check "servers" config.Servers
	for _, server := range config.Servers {
		if server.ServerIDs != nil {
			for _, subserver := range *server.ServerIDs {
				validateSource(getServerErr, subserver, "subserver", &invalidServers)
			}
		}
		if server.ServerID != "" {
			validateSource(getServerErr, server.ServerID, "server", &invalidServers)
		}
	}
	//(SV) Check "categories" config.Categories
	for _, category := range config.Categories {
		if category.CategoryIDs != nil {
			for _, subcategory := range *category.CategoryIDs {
				if validateSource(getChannelErr, subcategory, "subcategory", &invalidCategories) {
					checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
						subcategory, "subcategory", &missingPermsCategories)
				}
			}

		} else {
			if validateSource(getChannelErr, category.CategoryID, "category", &invalidCategories) {
				checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
					category.CategoryID, "category", &missingPermsCategories)
			}
		}
	}
	//(SV) Check "channels" config.Channels
	for _, channel := range config.Channels {
		if channel.ChannelIDs != nil {
			for _, subchannel := range *channel.ChannelIDs {
				if validateSource(getChannelErr, subchannel, "subchannel", &invalidChannels) {
					checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
						subchannel, "subchannel", &missingPermsChannels)
					checkChannelPerm(discordgo.PermissionReadMessageHistory, "PermissionReadMessageHistory",
						subchannel, "subchannel", &missingPermsChannels)
					if channel.ReactWhenDownloaded != nil {
						if *channel.ReactWhenDownloaded {
							checkChannelPerm(discordgo.PermissionAddReactions, "PermissionAddReactions",
								subchannel, "subchannel", &missingPermsChannels)
						}
					}
				}
			}

		} else {
			if validateSource(getChannelErr, channel.ChannelID, "channel", &invalidChannels) {
				checkChannelPerm(discordgo.PermissionViewChannel, "PermissionViewChannel",
					channel.ChannelID, "channel", &missingPermsChannels)
				checkChannelPerm(discordgo.PermissionReadMessageHistory, "PermissionReadMessageHistory",
					channel.ChannelID, "channel", &missingPermsChannels)
				if channel.ReactWhenDownloaded != nil {
					if *channel.ReactWhenDownloaded {
						checkChannelPerm(discordgo.PermissionAddReactions, "PermissionAddReactions",
							channel.ChannelID, "channel", &missingPermsChannels)
					}
				}
			}
		}
	}
	//(SV) NOTE: No validation for users because no way to do that by just user ID from what I've seen.

	//(SV) Output Invalid Sources
	invalidSources := len(invalidAdminChannels) + len(invalidChannels) + len(invalidCategories) + len(invalidServers)
	if invalidSources > 0 {
		log.Println(lg("Discord", "Validation", color.HiRedString,
			"Found %d invalid sources in configuration...", invalidSources))
		logMsg := fmt.Sprintf("Validation found %d invalid sources...\n", invalidSources)
		if len(invalidAdminChannels) > 0 {
			logMsg += fmt.Sprintf("\n**- Admin Channels: (%d)** - %s",
				len(invalidAdminChannels), strings.Join(invalidAdminChannels, ", "))
		}
		if len(invalidServers) > 0 {
			logMsg += fmt.Sprintf("\n**- Download Servers: (%d)** - %s",
				len(invalidServers), strings.Join(invalidServers, ", "))
		}
		if len(invalidCategories) > 0 {
			logMsg += fmt.Sprintf("\n**- Download Categories: (%d)** - %s",
				len(invalidCategories), strings.Join(invalidCategories, ", "))
		}
		if len(invalidChannels) > 0 {
			logMsg += fmt.Sprintf("\n**- Download Channels: (%d)** - %s",
				len(invalidChannels), strings.Join(invalidChannels, ", "))
		}
		sendErrorMessage(logMsg)
	} else {
		log.Println(lg("Discord", "Validation", color.HiGreenString,
			"No ID issues detected! Bot can see all configured Discord sources, but that doesn't check Discord permissions..."))
	}
	//(SV) Output Discord Permission Issues
	missingPermsSources := len(missingPermsAdminChannels) + len(missingPermsCategories) + len(missingPermsChannels)
	if missingPermsSources > 0 {
		log.Println(lg("Discord", "Validation", color.HiRedString,
			"Found %d sources with insufficient Discord permissions...", missingPermsSources))
		log.Println(lg("Discord", "Validation", color.HiRedString,
			"The bot will still function, these are just warnings..."))
	} else {
		log.Println(lg("Discord", "Validation", color.HiGreenString,
			"No permission issues detected! Bot seems to have all required Discord permissions."))
	}

	mainWg.Done()
}

//#endregion
