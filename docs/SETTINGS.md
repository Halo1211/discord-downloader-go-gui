# Settings Guide

Settings can be stored as JSON, JSONC, or YAML. The GUI edits the active file,
validates it, writes it atomically, and keeps a `.bak` copy of the previous
file. Restart the application after changing launch-only GUI options such as
the address.

For a complete safe starter file, copy
[`settings.example.json`](../settings.example.json) and replace every
`REPLACE_WITH_...` placeholder. The template follows the configuration model
documented by the original project while using only genuine Discord bot
authentication for this maintained fork.

## Required values

At minimum, configure a bot token and a destination rule:

```json
{
  "credentials": {
    "token": "YOUR_DISCORD_BOT_TOKEN"
  },
  "all": {
    "destination": "D:\\Discord Archive"
  }
}
```

`all` applies to every visible channel. For narrower control, use `servers`,
`categories`, `channels`, or `users` instead. A source entry uses Discord IDs
and a destination folder:

```json
{
  "servers": [
    {
      "server": "123456789012345678",
      "destination": "D:\\Discord Archive\\My Server",
      "enabled": true,
      "save": true
    }
  ]
}
```

Enable Developer Mode in Discord to copy server, category, channel, and user
IDs. A server rule can cover every channel visible to the bot. A channel rule
can override the server rule for a specific channel.

## GUI options

| Option | Default | Purpose |
| --- | ---: | --- |
| `guiEnabled` | `true` | Enable the local dashboard. |
| `guiAddress` | `127.0.0.1:7744` | Loopback host and port for the dashboard. |
| `guiOpenBrowser` | `true` | Open the dashboard at startup. |
| `guiAutoStart` | `false` | Start the runtime without pressing Start. |
| `guiCloseToTray` | `true` | Offer hide-to-tray when the window closes. |
| `guiStartMinimized` | `false` | Start the native Windows window minimized. |

The GUI address must remain on localhost or another loopback address. Do not
expose the dashboard directly to a LAN or the public internet.

## Throughput and limits

| Option | Default | Purpose |
| --- | ---: | --- |
| `downloadWorkers` | `4` | Concurrent real-time downloads. |
| `historyDownloadWorkers` | `4` | Concurrent files during history scans. |
| `downloadQueueSize` | `256` | Maximum queued real-time downloads. |
| `downloadMaxFileSize` | `536870912` | Maximum response bytes per file. |
| `downloadMaxRedirects` | `5` | Maximum redirects per request. |
| `downloadRetryMax` | `2` | Retries for temporary transfer failures. |
| `downloadTimeout` | `60` | Download timeout in seconds. |
| `historyMaxJobs` | `3` | Concurrent history channels. |
| `historyRequestCount` | `100` | Messages requested per Discord page. |

The queue capacity must be at least the number of real-time workers. Increase
worker counts gradually and watch rate limits, disk throughput, and memory.

## Storage and filters

Set `subfolders` to templates such as `{{fileType}}` to separate images,
videos, audio, text, and other files. `filenameFormat` supports placeholders
including `{{date}}`, `{{file}}`, `{{serverName}}`, and `{{channelName}}`.

Filters can allow or block phrases, users, roles, domains, extensions,
filenames, linked content, and reactions. Allow rules and block rules should be
tested with a small archive before a large history scan.

## Secrets and integrations

Twitter/X, Instagram, and Flickr integrations are optional. Prefer direct CDN
links and public media when possible. If an integration requires credentials,
keep them in the local settings file and never include that file in a release
bundle or bug report. Legacy Discord email/password fields are not a supported
authentication path for this fork; use a bot token.
