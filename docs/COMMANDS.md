# Commands Guide

Commands are enabled per source and use the configured prefix. The default
prefix is `ddg `, including the trailing space. Examples below assume the
default.

## Utility and information

| Command | Description |
| --- | --- |
| `ddg ping` | Report gateway latency and message round-trip time. |
| `ddg help` | Show commands available to the current user. |
| `ddg status` | Show runtime, connection, and binding information. |
| `ddg stats` | Show total downloads and downloads for the current channel. |
| `ddg exit` | Request a safe application shutdown when permitted. |

`ping` also accepts the legacy alias `test`; `help` accepts `commands`.

## History archive

Run `ddg history` in a bound channel to archive that channel. A target can be
a channel ID, category ID, server ID, configured alias, or `all`:

```text
ddg history
ddg history 123456789012345678
ddg history all
ddg history 123456789012345678 --since=30d
ddg history 123456789012345678 --before=2025-01-01
```

Useful subcommands and options:

- `list`, `status`, or `output` — list active and completed history jobs.
- `cancel` or `stop` — request cancellation for the current target.
- `--since=<date|duration|snowflake>` — start at a lower message boundary.
- `--before=<date|duration|snowflake>` — stop at an upper message boundary.
- `dbwipe` or `wipedb` — clear download records before processing.
- `cachewipe` or `wipecache` — remove the target history checkpoint.
- `help` or `info` — show the history command help response.

History scans are restricted to bot administrators. Configure administrator
user IDs in `admins` and, if needed, limit command access with
`adminChannels`. Do not use `dbwipe` or `cachewipe` unless you understand the
re-download and resume consequences.

## Per-channel controls

Source rules can disable commands with `allowCommands`. The command prefix can
be overridden globally or on a particular source with `commandPrefix`. Changes
to command configuration are applied after the configuration is reloaded.
