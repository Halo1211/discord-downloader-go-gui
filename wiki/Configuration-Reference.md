# Configuration Reference

This page maps the configuration model in `config.go` to the behavior exposed
by the GUI. Omitted values receive defaults. JSON and JSONC use quoted keys and
backslashes escaped as `\\`; YAML uses the same field names without JSON
punctuation.

## File and path discovery

| Key | Meaning |
| --- | --- |
| `overwriteCachePath` | Cache root; default `cache`. |
| `overwriteHistoryPath` | History checkpoints; default `cache/history`. |
| `overwriteDuploPath` | Image similarity catalog; default `cache/.duplo`. |
| `overwriteTwitterPath` | Twitter/X session cache; default `cache/twitter.json`. |
| `overwriteInstagramPath` | Instagram session cache; default `cache/instagram.json`. |
| `overwriteConstantsPath` | Expanded constants cache; default `cache/constants.json`. |
| `overwriteDatabasePath` | Download database; default `database`. |
| `overwriteDatabaseBackupsPath` | Database zip backups; default `backups`. |
| `logOutput` | Optional file for application logs. |

The executable also accepts a positional settings base or `--config=<base>`.
The extension determines JSON, JSONC, or YAML parsing. The application writes
conversion/cache files beneath `cache/` and should be allowed to create those
directories.

## Runtime, GUI, and Discord behavior

| Group | Keys |
| --- | --- |
| Logging | `verbose`, `debug`, `debugExtra`, `settingsOutput`, `logIndent`, `messageOutput`, `messageOutputHistory`, `discordLogLevel`. |
| GUI | `guiEnabled`, `guiAddress`, `guiOpenBrowser`, `guiAutoStart`, `guiCloseToTray`, `guiStartMinimized`. |
| Runtime | `processLimit`, `watchSettings`, `backupDatabaseOnStart`, `checkupRate`, `connectionCheck`, `connectionCheckRate`, `githubUpdateChecking`, `exitOnBadConnection`. |
| Discord | `scanEdits`, `ignoreBots`, `scanOwnMessages`, `allowCommands`, `allowGeneralCommands`, `commandPrefix`, `commandTagging`, `discordTimeout`. |
| Downloads | `downloadTimeout`, `downloadRetryMax`, `downloadWorkers`, `downloadQueueSize`, `downloadMaxFileSize`, `downloadMaxRedirects`, `sendErrorMessages`. |
| Emoji/stickers | `ignoreEmojis`, `ignoreStickers`, `ignoreEmojisWEBP`, `emojisServers`, `emojisFilenameFormat`, `emojisDestination`, `stickersServers`, `stickersFilenameFormat`, `stickersDestination`. |

The GUI address is deliberately limited to localhost/loopback. Do not expose
the dashboard through a LAN address or reverse proxy without adding a separate
authentication and transport boundary.

## Archive and filename behavior

| Key | Meaning |
| --- | --- |
| `save` | Master switch for saving matched media. |
| `saveImages`, `saveVideos`, `saveAudioFiles`, `saveTextFiles`, `saveOtherFiles` | Content groups to keep. |
| `savePossibleDuplicates` | Save when the exact filename already exists. |
| `subfolders` | Ordered relative folder templates, commonly `{{fileType}}`. |
| `subfoldersFallback` | Fallback templates when a primary value is unavailable. |
| `filenameDateFormat` | Go time layout used by `{{date}}`; default `2006-01-02_15-04-05`. |
| `filenameFormat` | Default `{{date}} {{file}}`. |
| `filepathNormalizeText` | Normalize accented/special letter forms. |
| `filepathStripSymbols` | Remove symbols from generated paths. |
| `delayHandling` | Delay live link handling in milliseconds. |
| `delayHandlingHistory` | Delay history link handling in milliseconds. |

Template values include the source filename and date values. Keep generated
paths relative to the configured destination; the program sanitizes path
components and rejects unsafe path traversal.

## Filters and deduplication

`filters` supports paired allow/block lists:

```json
{
  "filters": {
    "allowedDomains": ["cdn.discordapp.com"],
    "blockedExtensions": [".exe", ".dll"],
    "blockedUsers": ["123456789012345678"],
    "blockedFilenames": ["private"]
  }
}
```

Available lists are `blockedPhrases`/`allowedPhrases`,
`blockedUsers`/`allowedUsers`, `blockedRoles`/`allowedRoles`,
`blockedLinkContent`/`allowedLinkContent`, `blockedDomains`/`allowedDomains`,
`blockedExtensions`/`allowedExtensions`, `blockedFilenames`/`allowedFilenames`,
and `blockedReactions`/`allowedReactions`. An allow-list narrows processing:
items not matching an enabled allow-list are rejected.

Set `duplo` to `true` to compare image similarity using the cached catalog.
`duploThreshold` controls the comparison threshold and should be tuned with a
small sample. The catalog does not automatically inspect every pre-existing
image.

## History and forwarding

| Key | Meaning |
| --- | --- |
| `historyMaxJobs` | Concurrent history channel jobs. |
| `historyDownloadWorkers` | File workers within history processing. |
| `autoHistory` | Queue a history job on startup for a source. |
| `autoHistoryExit` | Exit after startup history jobs complete. |
| `autoHistoryBefore`, `autoHistorySince` | Optional date boundaries. |
| `historyRequestCount` | Messages per page, 1–100. |
| `historyRequestDelay` | Delay between history pages. |
| `sendHistoryStatus`, `sendAutoHistoryStatus` | Discord status messages. |
| `outputHistoryStatus`, `outputHistoryErrors` | Console/log status output. |
| `sendFileToChannel`, `sendFileToChannels` | Optional forwarding destinations. |
| `sendFileDirectly`, `sendFileCaption` | Forwarding mode and caption. |

For logs, `logLinks` and `logMessages` accept a destination, subfolder and
filename template, prefix/suffix, duplicate filtering, and download/failure
logging switches. Be careful: logs can contain message text, usernames, URLs,
and local paths.

## Source rule shape

Every entry in `all`, `users`, `servers`, `categories`, and `channels` can use
an ID or an ID list, a destination, aliases, and source-level overrides:

```json
{
  "servers": [
    {
      "server": "123456789012345678",
      "destination": "D:\\Discord Archive\\Server",
      "alias": "work",
      "serverBlacklist": ["234567890123456789"],
      "save": true,
      "enabled": true
    }
  ]
}
```

Source-level overrides include `enabled`, `save`, `allowCommands`, `scanEdits`,
`ignoreBots`, `commandPrefix`, media switches, delays, filters, reactions,
history options, forwarding, and log settings. Start with one channel and
expand only after the output is correct.
