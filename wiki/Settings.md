# Settings

Settings are supported in JSON, JSONC, and YAML. The GUI validates the active
file and writes it atomically, keeping a backup of the previous version.

## Configuration hierarchy

Rules can be defined globally or scoped to a server, category, channel, or
user. More-specific rules can override broader defaults. Start with `all` for
a small test server, then narrow the rule before archiving a large server.

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

## Essential options

| Option | Purpose |
| --- | --- |
| `credentials.token` | Genuine Discord bot token. Treat it as a password. |
| `all.destination` | Default folder for files matched by the global rule. |
| `channels` | Channel-specific archive rules. |
| `servers` / `categories` / `users` | Other scoped rules. |
| `saveImages`, `saveVideos`, `saveAudioFiles` | Enable media groups. |
| `subfolders` | Optional content-type folder template. |
| `filenameFormat` | Filename template for saved files. |

## GUI and performance defaults

| Option | Default | Notes |
| --- | ---: | --- |
| `guiAddress` | `127.0.0.1:7744` | Keep the dashboard on loopback. |
| `guiAutoStart` | `false` | Start manually until setup is verified. |
| `downloadWorkers` | `4` | Increase gradually while watching rate limits. |
| `downloadQueueSize` | `256` | Maximum queued real-time transfers. |
| `downloadMaxFileSize` | `536870912` | 512 MiB response limit. |
| `downloadMaxRedirects` | `5` | Redirect limit per request. |
| `historyDownloadWorkers` | `4` | Concurrent history file transfers. |
| `historyMaxJobs` | `3` | Concurrent history channels. |

The full option catalog and GUI editor are available in
[`docs/SETTINGS.md`](../docs/SETTINGS.md).

## Secrets and integrations

Optional Twitter/X, Instagram, and Flickr integrations may require separate
credentials or API keys. Keep those values local and never include them in a
release, screenshot, issue, or log upload. Direct Discord CDN links are the
simplest and most reliable source.
