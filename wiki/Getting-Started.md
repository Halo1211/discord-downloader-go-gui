# Getting Started

## 1. Create the Discord bot

1. Open the [Discord Developer Portal](https://discord.com/developers/applications).
2. Create an application and add a bot.
3. Enable **Message Content Intent** under **Privileged Gateway Intents**.
4. Create an OAuth2 bot invite for the servers that should be monitored.
5. Grant `View Channel` and `Read Message History`. Add `Send Messages` for
   commands/status messages and `Add Reactions` when download reactions are
   enabled.

The bot must be able to see a channel before it can process that channel.
Message Content Intent is required for message and URL discovery.

## 2. Install the release

Download the single Windows amd64 executable from the
[latest release](https://github.com/Halo1211/discord-downloader-go-gui/releases/latest).
Put it in its own application folder. Do not place it beside unrelated files.

Copy `settings.example.json` to `settings.json`, then replace the token and
destination placeholders:

```json
{
  "credentials": { "token": "YOUR_DISCORD_BOT_TOKEN" },
  "all": { "destination": "D:\\Discord Archive" }
}
```

## 3. Find Discord IDs

Enable **Developer Mode** in Discord under **User Settings → Advanced**.
Right-click a server, category, channel, or user and choose **Copy ID**.
Use IDs in `servers`, `categories`, `channels`, or `users` rules when the
global `all` rule is too broad.

## 4. Start the program

Run the executable from PowerShell:

```powershell
.\discord-downloader-go-gui-v1.0.0-windows-amd64.exe
```

The dashboard opens in a waiting state. Review the settings and press
**Start application**. Set `guiAutoStart` to `true` only after the bot and
archive destination have been tested.

The GUI listens on `127.0.0.1:7744` by default. Use `--gui-browser` to open
the dashboard in the system browser, or `--no-gui` for terminal-only mode.

## 5. Verify the first download

Post a supported file or URL in a bound channel. Confirm that the activity
panel reports the transfer and that the file appears under the configured
destination. If it does not, check [Troubleshooting](Troubleshooting.md).

## Keep credentials safe

The GUI masks secret fields. Leaving a masked secret blank preserves the
stored value. Do not commit `settings.json`, even when it appears to contain
an empty or masked token.
