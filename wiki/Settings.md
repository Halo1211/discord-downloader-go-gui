# Settings

Settings are supported in JSON, JSONC, and YAML. The default base name is
`settings`, so the application searches for `settings.json`, `settings.jsonc`,
or `settings.yaml`/`settings.yml`. A positional base name or `--config` can
select another file.

## Safe editing workflow

1. Copy [`settings.example.json`](../settings.example.json) to `settings.json`.
2. Change only the bot token, destination, and source IDs for the first test.
3. Keep the file private and do not commit it.
4. Use the dashboard's normal mode for common values and advanced mode for the
   complete schema.
5. Save, review validation warnings, and restart when a setting requires a
   restart.

The GUI validates worker, queue, timeout, retry, file-size, history, and local
GUI-address limits before writing. It writes through a temporary file, creates
`settings.json.bak` when replacing an existing file, and masks secrets when
reading them back. Leaving a masked secret blank preserves its stored value.

## Configuration hierarchy

Rules can be defined globally or scoped to a server, category, channel, or
user. A source entry can contain one ID (`server`, `channel`, etc.) or a list
(`servers`, `channels`, etc.). More-specific rules are checked before broader
rules. A channel rule is the most direct way to give one channel a different
destination or filter.

```json
{
  "all": {
    "destination": "D:\\Discord Archive",
    "saveImages": true,
    "saveVideos": true
  },
  "channels": [
    {
      "channel": "123456789012345678",
      "destination": "D:\\Discord Archive\\Important",
      "enabled": true
    }
  ]
}
```

Use `allBlacklistUsers`, `allBlacklistServers`,
`allBlacklistCategories`, and `allBlacklistChannels` to carve exclusions out
of a broad global rule.

## Essential options

| Option | Purpose |
| --- | --- |
| `credentials.token` | Genuine Discord bot token; treat it as a password. |
| `admins` | Discord user IDs allowed to run admin commands. |
| `adminChannels` | Optional command/status channels and permission behavior. |
| `all.destination` | Default folder for files matched by the global rule. |
| `channels` | Channel-specific archive rules. |
| `servers`, `categories`, `users` | Other scoped rules. |
| `saveImages`, `saveVideos`, `saveAudioFiles` | Enable media groups. |
| `subfolders` | Relative folder templates under a destination. |
| `filenameFormat` | Output filename template. |
| `filters` | Allow/block phrases, IDs, domains, extensions, names, and reactions. |

## Important defaults and limits

| Setting | Default | Validation or effect |
| --- | ---: | --- |
| `guiAddress` | `127.0.0.1:7744` | Must remain a localhost/loopback address. |
| `guiAutoStart` | `false` | Manual Start is recommended for first run. |
| `downloadWorkers` | `4` | 1–128. |
| `downloadQueueSize` | `256` | At least the worker count; maximum 1,000,000. |
| `downloadTimeout` | `60` sec | Minimum 10 seconds. |
| `discordTimeout` | `180` sec | Minimum 10 seconds. |
| `downloadRetryMax` | `2` | 1–100. |
| `downloadMaxFileSize` | `512 MiB` | Must be positive. |
| `downloadMaxRedirects` | `5` | Must not be negative. |
| `historyDownloadWorkers` | `4` | 1–128. |
| `historyMaxJobs` | `3` | 1–64. |
| `historyRequestCount` | `100` | 1–100 messages per Discord request. |
| `commandPrefix` | `ddg ` | May be overridden per source. |
| `ignoreBots` | `true` | Prevents bot-authored messages from being processed. |

The complete starter file includes every commonly useful option and safe
placeholders. For the field-by-field catalog, see
[Configuration Reference](Configuration-Reference.md).

## Bot-only credential policy

The struct retains legacy email/password fields so old files can be migrated,
but the current login path rejects email/password Discord login. Configure a
real bot token instead. Optional Twitter/X, Instagram, and Flickr credentials
belong to their source resolvers and must not be confused with Discord login.
