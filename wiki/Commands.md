# Commands

Commands use the configured prefix. The default prefix is `ddg `, including
the trailing space. Examples below assume the default.

| Command | Permission | Description |
| --- | --- | --- |
| `ddg help` | configured users | List available commands. |
| `ddg ping` | configured users | Report gateway latency. |
| `ddg status` | configured users | Show runtime and binding status. |
| `ddg stats` | configured users | Show download totals. |
| `ddg history` | bot administrators | Archive older messages. |
| `ddg exit` | bot administrators | Request a safe shutdown. |

Legacy aliases include `test` for `ping` and `commands` for `help`.

## History examples

```text
ddg history
ddg history 123456789012345678
ddg history 123456789012345678 --since=30d
ddg history 123456789012345678 --before=2025-01-01
ddg history status
ddg history cancel
```

Targets may be channel IDs, category IDs, server IDs, configured aliases, or
`all`. History controls are restricted to administrators. See
[History](History.md) before using `dbwipe` or `cachewipe`.
