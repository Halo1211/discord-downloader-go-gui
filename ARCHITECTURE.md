# Architecture map

## Runtime flow

```text
main.go
  +-- config.go       load JSON/JSONC/YAML, apply defaults, watch changes
  +-- database.go     open tiedot database and deduplication indexes
  +-- parse.go        optional Twitter/Instagram/source resolvers
  +-- discord.go      Discord login, intents, state, permissions, status
  +-- commands.go     command router and history/download commands
  +-- handlers.go     MessageCreate/MessageUpdate filtering and dispatch
  |     +-- downloads.go
  |           +-- collect attachments, embeds, snapshots, URLs, emoji, stickers
  |           +-- resolve supported source URLs
  |           +-- apply domain/filename/extension/type/reaction filters
  |           +-- real-time queue -> bounded workers; history -> bounded batch workers
  |           +-- HTTP download -> bounded response -> atomic filesystem write -> database -> reaction/logging
  +-- history.go      paginate channel/thread messages in batches of up to 100
  +-- gui.go          optional loopback dashboard and guarded local API
```

## Main state boundaries

- `config` is the runtime configuration snapshot. Defaults are applied before
  startup; the file watcher reports later edits and a restart applies them
  without racing active Discord, download, history, or database work.
- `bot` is the DiscordGo session. It supplies gateway events and REST calls;
  history uses REST pagination while real-time operation uses gateway events.
- `myDB` stores normalized source URLs, destination metadata, channel IDs and
  user IDs. A startup-built URL/channel index makes repeated checks O(1).
- `historyJobs` is the in-memory scheduler. Each channel has at most one active
  history job and can resume from its JSON cache under `cache/history/`.
- Real-time downloads use a bounded worker queue. Duplicate URLs already queued
  for the same channel are coalesced. History batches use their own bounded
  workers, but promote checkpoints only after every selected message completes.
- The GUI control surface opens automatically and binds to `127.0.0.1:7744` by
  default. The downloader runtime waits for the dashboard's **Start** button
  before opening the database, workers, or external connections; `guiAutoStart`
  or `--gui-auto-start` restores unattended startup. On
  Windows the loopback page is hosted inside the same process in a native
  WebView2 window; the system browser is only a fallback. It exposes runtime
  status, a bounded live-log console, token-protected history actions, and a reflection-backed complete
  configuration editor. Settings saves are validated, backed up, and atomically
  promoted. Credential fields are redacted on reads and blank values preserve
  existing secrets. On Windows, closing the window hides it in the system tray
  by default. `--no-gui` keeps a CLI-only deployment available and starts the
  runtime immediately.

## Discord requirements

Use a genuine Discord bot token. The bot must be invited to every server/channel
to archive and needs `VIEW_CHANNEL` and `READ_MESSAGE_HISTORY`; reactions,
status messages, and file forwarding need their respective extra permissions.
Enable the **Message Content Intent** in the Developer Portal. Without it,
Discord can return empty message content, embeds, and attachments.
The application uses DiscordGo v0.29 and explicitly rebuilds its exported REST
roots for Discord API/Gateway v10.

Automated normal user accounts/self-bots are intentionally not a supported
deployment target. Discord prohibits them and may terminate the account.

## Persistence and recovery

Download URLs are normalized for Discord CDN signatures (`ex`, `is`, `hm`) before
database lookup/write. This makes a re-scan idempotent even when Discord returns
a refreshed signed URL. Interrupted history jobs record their last completed page
in `cache/history/<channel-id>.json` and resume from that checkpoint. Completed
unfiltered scans also store the newest fully covered message ID, allowing later
scans to stop without traversing older messages again. Cache files
and downloaded files are written through temporary files and atomically renamed
after completion, preventing truncated artifacts after a crash.
