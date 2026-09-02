# Troubleshooting

## The dashboard says a settings file was created

Open the generated `settings.json`, add a genuine bot token and a destination
rule, save it, and restart the application. Do not paste a real token into an
issue or screenshot.

## The bot connects but downloads nothing

Check that the bot can view the channel, read message history, and use Message
Content Intent. Confirm that the channel is covered by a rule, the relevant
media type is enabled, and the destination is writable.

## History is slow or stops

Check Discord rate limits, disk space, and the application console. Reduce
`historyDownloadWorkers` or `historyMaxJobs` on a small machine. A checkpoint
can be reused after a restart.

## The GUI does not open

Use `--gui-browser` to open the local dashboard in the system browser. Confirm
that the configured address is loopback and that the port is not already in
use. `--no-gui` runs the bot without the dashboard.

## A source resolver fails

Try the original public URL, confirm the provider is reachable, and check the
logs for the resolver name. Provider changes may require a future update; a
direct Discord attachment is the preferred fallback.
