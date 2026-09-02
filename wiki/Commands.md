# Commands

Commands use the configured prefix. The default prefix is `ddg `, including
the trailing space. Examples below assume the default.

## Access rules

The application must be allowed to read the message and send a reply when a
reply is expected. General commands are enabled by `allowGeneralCommands` by
default. If that setting is disabled, commands are accepted in registered
`adminChannels` or in a configured source when `allowCommands` is enabled.

Admin operations additionally require the author to be listed in `admins`.
When no explicit administrators or admin channels are configured, the bot's
own user ID is the only administrator. An admin channel with
`unlockCommands: true` can bypass the author check for command access.

## Utility and information commands

| Command | Alias | Description |
| --- | --- | --- |
| `ddg ping` | `test` | Reply with Gateway latency and message round-trip time. |
| `ddg help` | `commands` | Show commands visible to the current user. |
| `ddg status` | — | Show uptime, connected servers, and configured bindings. |
| `ddg stats` | — | Show total downloads and downloads for the current channel. |

`stats` only reports channel statistics when the source rule allows commands.
`help` hides admin commands from users who are not bot administrators.

## History command

Run this in a bound channel:

```text
ddg history
ddg history 123456789012345678
ddg history all
ddg history Important-Archive
ddg history 123456789012345678 --since=30d
ddg history 123456789012345678 --before=2025-01-01
```

Targets can be a channel ID, category ID, server ID, configured alias, or
`all`. A server target expands to its text/news/forum-like channels; category
targets expand to channels in that category.

| Subcommand or option | Behavior |
| --- | --- |
| `list`, `status`, `output` | List waiting, running, completed, cancelled, and failed jobs. |
| `cancel`, `stop` | Request cancellation for the target job. |
| `help`, `info` | Show history help. |
| `--since=<date>` | Use a lower message boundary, such as `2025-01-01`. |
| `--before=<date>` | Use an upper message boundary, such as `2025-01-01`. |
| `--since=<duration>` | Use a relative boundary, such as `30d` or `12h`. |
| `--before=<duration>` | Use a relative upper boundary. |
| `--since=<snowflake>` | Use a Discord message ID as the boundary. |
| `--before=<snowflake>` | Use a Discord message ID as the boundary. |
| `dbwipe`, `wipedb` | Delete download records for the target, or the whole DB with `all`. |
| `cachewipe`, `wipecache` | Delete the target history checkpoint, or all checkpoints with `all`. |

History operations are bot-admin-only. `dbwipe` allows files to be considered
new again; `cachewipe` removes resume progress. Back up `database/` before
using either destructive subcommand.

## Shutdown commands

| Command | Alias | Permission | Description |
| --- | --- | --- | --- |
| `ddg exit` | `kill`, `reload` | Bot administrator | Request a graceful application shutdown. |

The `reload` alias does not restart the process by itself. A process manager
can restart the executable after it exits.

## Per-source overrides

`allowCommands` can disable commands for a source, and `commandPrefix` can
override the global prefix for a source or admin channel. Configuration edits
are reloaded by the watcher; start-time GUI flags still require a process
restart.
