# Reference Media

## Current application screenshot

The repository README uses the current Archive Relay dashboard screenshot:

![Archive Relay dashboard](assets/dashboard.png)

The dashboard is a local UI for configuration, readiness checks, transfer
activity, history jobs, and the application console. Text shown in a screenshot
is illustrative; never include a real token, private channel names, or private
media in a public screenshot.

## Original wiki references

This fork reviewed the original
[`get-got/discord-downloader-go` wiki](https://github.com/get-got/discord-downloader-go/wiki)
and retained two useful, public references:

- The [Message Content Intent screenshot](assets/original-message-content-intent.png)
  is a historical Developer Portal visual. Discord's UI and intent policy can
  change, so follow the current [Gateway intents documentation](https://docs.discord.com/developers/events/gateway).
- The [historical tutorial thumbnail](assets/tutorial-video-thumbnail.jpg)
  links to the original [YouTube tutorial](https://www.youtube.com/watch?v=06UUXDQ80f8).

The original wiki also contained user-token extraction screenshots. They are
intentionally excluded from this maintained wiki: this fork documents genuine
Discord bot authentication only, and Discord's current OAuth2 documentation
states that bot tokens must be treated like passwords and never exposed.

## Attribution and scope

The retained assets are included for historical documentation and attribution
to the original project's public wiki. They are not a recommendation to copy
the original project's legacy login flow. The maintained instructions are
[Getting Started](Getting-Started.md), [Settings](Settings.md), and
[Troubleshooting](Troubleshooting.md).
