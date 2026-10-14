# Release Notes

## v1.0.1

This maintenance update improves download, history, configuration, and GUI
behavior. It also adds regression coverage for the bugs fixed since v1.0.0.

### Fixes

- Handle incomplete Discord events and temporary disconnects without stopping
  downloads, commands, or message logging.
- Apply source scope and blacklist rules consistently, and keep admin-only
  history status private.
- Coordinate pending downloads with duplicate checks and history scans so a
  file is not skipped before its database record is committed.
- Preserve history cancellation and checkpoints when a scan is interrupted or
  a channel lookup fails.
- Report settings read/write failures accurately and preserve existing secrets
  when saving through the GUI.
- Reject malformed GUI addresses and unsafe date inputs; keep resolver API keys
  out of HTTP error messages.

### Documentation and CI

- Keep the user guides in English and make executable names consistent across
  the README, getting-started guides, and release workflow.
- Remove obsolete tutorial media and duplicate documentation.
- Run CI on branch pushes as well as pull requests.

No configuration migration is required. Back up `settings.json` and `database/`
before upgrading, especially before a large history scan.

## v1.0.0

The first release of the maintained GUI fork added the Windows dashboard,
loopback API, resumable history jobs, bounded download queues, and a single
Windows amd64 executable. Earlier `v2.6.x` tags belong to the project's
previous version line; this fork restarted its version line at v1.0.0.
