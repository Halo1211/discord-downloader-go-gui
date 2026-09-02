# Discord Downloader Go GUI Wiki

Welcome to the maintained documentation for Discord Downloader Go GUI. This
wiki is based on the original project's documentation structure, updated for
the GUI fork, the Windows single-executable release, and Discord's current
bot-account requirements.

## Read next

- [Getting Started](Getting-Started.md) — create a bot, configure permissions,
  and complete the first run.
- [Settings](Settings.md) — understand the configuration hierarchy and the
  most important options.
- [Settings Examples](Settings-Examples.md) — safe copy-and-edit templates.
- [Commands](Commands.md) — bot commands and their permissions.
- [History](History.md) — archive older messages, checkpoints, and recovery.
- [Sources](Sources.md) — supported media sources and resolver limitations.
- [Configuration Reference](Configuration-Reference.md) — complete option groups,
  defaults, limits, and template placeholders.
- [CLI and GUI](CLI-and-GUI.md) — runtime flags, dashboard workflow, and local
  security model.
- [Reference Media](Media-References.md) — dashboard screenshot, original wiki
  attribution, and the historical tutorial link.
- [Troubleshooting](Troubleshooting.md) — common setup and runtime problems.
- [Development](Development.md) — tests, builds, and release packaging.

## Important safety rule

Use a genuine Discord bot account and bot token. Do not use a normal Discord
user account, self-bot, or user-bot. Automated user accounts can violate
Discord's rules and put the account at risk. Never share `settings.json`,
tokens, cookies, passwords, logs containing secrets, or downloaded private
media.

## Release model

The maintained release is distributed as one Windows amd64 executable. It is
intended to run beside `settings.json`, `cache/`, `database/`, and the chosen
archive folders. The maintainer's local `release/` directory is ignored by
Git and is not part of the source repository.

## Original project

This fork began from
[get-got/discord-downloader-go](https://github.com/get-got/discord-downloader-go),
whose wiki provided the original navigation model. The original project is
archived; this repository's guides describe the maintained GUI fork and may
intentionally differ where safety, defaults, or supported workflows changed.
