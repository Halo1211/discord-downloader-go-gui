# Release Notes — v1.0.0

This release packages the maintained GUI fork at
`Halo1211/discord-downloader-go-gui`.

## Presentation

- Refreshed the README with a dashboard preview, release badges, and a new repository logo.
- Added a clean vector logo asset that can be reused in project documentation and packaging.
- Embedded the repository logo directly into the desktop dashboard so the program is self-contained.
- The embedded logo adapts its colors to the selected dashboard theme.

## Highlights

- Windows WebView2 dashboard with optional browser fallback.
- System-tray hide/restore flow with safe exit confirmation.
- Searchable Normal and Advanced settings editor.
- Token-protected loopback GUI API with masked credentials and atomic settings
  backups.
- Bounded real-time and history worker queues.
- Retry backoff, redirect limits, response-size limits, and HTTP connection
  reuse.
- Atomic download and history checkpoint writes.
- Resumable history jobs with cancellation and per-channel high-water marks.
- Discord API v10 endpoint compatibility and DiscordGo v0.29.
- GitHub Actions CI and tag-based single-executable Windows release builds.

The release distribution intentionally contains one Windows amd64 executable
to keep the desktop download simple. This release starts the maintained GUI
fork's version line at `v1.0.0`.

## Upgrade notes

Existing JSON/YAML settings remain supported. The first run can import a legacy
`config.ini`, but a bot token is the recommended authentication method. Review
the generated settings file before starting a large archive.

The local maintainer bundle is generated under `release/`. That directory is
explicitly ignored by Git and must not be committed.
