# Getting Started

This guide takes a new installation from a Discord bot application to the
first successful download.

## Discord setup

1. Create an application in the [Discord Developer Portal](https://discord.com/developers/applications).
2. Add a bot and copy its token once. Treat the token like a password.
3. Enable **Message Content Intent** under **Privileged Gateway Intents**.
4. Create an OAuth2 bot invite with `View Channel` and `Read Message History`.
5. Add `Send Messages` if the bot should answer commands or send error/status
   messages. Add `Add Reactions` if download reactions are enabled.
6. Invite the bot to every server that should be monitored.

The bot must be able to see a channel before the application can bind to it.
The Message Content Intent is required for message content, embeds, and URL
discovery.

## Install a release

Download the single Windows amd64 executable from the
[latest release](https://github.com/Halo1211/discord-downloader-go-gui/releases/latest)
and extract it to its own directory. Keep `settings.json`, `cache/`,
`database/`, and downloaded media beside the executable so relative paths are
predictable.

Copy `settings.example.json` to `settings.json` and replace both placeholders:

```json
{
  "credentials": {
    "token": "your-bot-token"
  },
  "all": {
    "destination": "D:\\Discord Archive"
  }
}
```

The full settings editor is available in the GUI. Values not present in the
file receive application defaults.

## First launch

On Windows, run:

```powershell
.\discord-downloader-go-gui.exe
```

The dashboard opens in a waiting state. Confirm the token and archive target,
then click **Start application**. With `guiAutoStart: true`, the runtime starts
automatically after the dashboard is ready.

For a terminal-only process:

```powershell
.\discord-downloader-go.exe --no-gui
```

Use `--config=my-settings` to load `my-settings.json` or `my-settings.yaml`
instead of the default file name.

## Verify the installation

After startup, confirm that the log contains a successful Discord connection
and that the dashboard shows the expected server count. Post a supported file
or URL in a bound channel. The first download should appear in the activity
panel and in the configured destination folder.

If nothing is downloaded, check the bot's channel permissions, the Message
Content Intent, the source rule, and whether `save` and the relevant file type
setting are enabled.

## Keep credentials safe

Never commit `settings.json`, tokens, cookies, passwords, databases, caches, or
downloaded media. Use `settings.example.json` when sharing configuration
templates. The GUI masks secret fields; leave a masked field blank to preserve
the existing value.
