# Settings Examples

All examples use placeholders. Replace IDs, paths, and the bot token before
starting the application.

The repository's [`settings.example.json`](../settings.example.json) is a
complete starter template with the main global, GUI, history, filter, logging,
and source options. The smaller examples below show common ways to narrow the
configuration after the first run.

## Minimal bot setup

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

## Separate servers and file types

```json
{
  "credentials": { "token": "YOUR_DISCORD_BOT_TOKEN" },
  "servers": [
    {
      "server": "111111111111111111",
      "destination": "D:\\Discord Archive\\Community",
      "saveImages": true,
      "saveVideos": true,
      "saveAudioFiles": false
    },
    {
      "server": "222222222222222222",
      "destination": "D:\\Discord Archive\\Projects",
      "subfolders": "{{fileType}}"
    }
  ]
}
```

## Channel-specific rule

```json
{
  "credentials": { "token": "YOUR_DISCORD_BOT_TOKEN" },
  "all": { "destination": "D:\\Discord Archive" },
  "channels": [
    {
      "channel": "333333333333333333",
      "destination": "D:\\Discord Archive\\Priority",
      "enabled": true,
      "savePossibleDuplicates": false
    }
  ]
}
```

## Safer rollout

1. Test one channel with a small `--since` range.
2. Confirm the destination and filename templates.
3. Enable additional media types one at a time.
4. Keep a backup of `database/` before destructive database operations.
