# Troubleshooting

## The dashboard says a settings file was created

Open the generated `settings.json`, add a genuine bot token and a destination
rule, save it, and restart the application. Do not paste a real token into an
issue or screenshot.

If the file is not found, check the executable's working directory and the
selected base name. `--config archive-test` searches for
`archive-test.json`, `archive-test.jsonc`, or `archive-test.yaml`.

## The bot fails to connect

Confirm that `credentials.token` is a current bot token and that the bot is
still installed in the target server. Email/password Discord login is not
supported by this fork. If the token was ever exposed, regenerate it in the
Developer Portal and update the local settings file.

If the log mentions a privileged intent, enable **Message Content Intent** on
the application's Bot page. The official [Gateway intent reference](https://docs.discord.com/developers/events/gateway)
lists the events and fields affected by privileged intents.

## The bot connects but downloads nothing

Check that the bot can view the channel, read message history, and use Message
Content Intent. Confirm that the channel is covered by a rule, the relevant
media type is enabled, and the destination is writable.

Also confirm that `save` is true, the matching media switch is true, and an
allow-list has not rejected the URL. A broad `all` rule can be blocked by an
`allBlacklist...` entry; a more-specific source rule can also set `enabled` or
`save` to false.

## History is slow or stops

Check Discord rate limits, disk space, and the application console. Reduce
`historyDownloadWorkers` or `historyMaxJobs` on a small machine. A checkpoint
can be reused after a restart.

## The GUI does not open

Use `--gui-browser` to open the local dashboard in the system browser. Confirm
that the configured address is loopback and that the port is not already in
use. `--no-gui` runs the bot without the dashboard.

The address must remain a localhost or loopback address. If the port is busy,
choose another loopback port, for example `--gui-address=127.0.0.1:8899`.

## A source resolver fails

Try the original public URL, confirm the provider is reachable, and check the
logs for the resolver name. Provider changes may require a future update; a
direct Discord attachment is the preferred fallback.

## Files are duplicated or missing

The database stores normalized source URLs and the history cache stores scan
checkpoints. Do not delete either while a job is running. Use
`ddg history cachewipe` only when you deliberately want to rescan a channel;
use `dbwipe` only after backing up `database/` and accepting that prior
downloads may be considered new.

## The application becomes slow

Lower `downloadWorkers`, `historyDownloadWorkers`, or `historyMaxJobs`, and
increase `historyRequestDelay` if Discord rate limits appear. Keep
`downloadQueueSize` at least as large as `downloadWorkers`; the GUI validates
this relationship before saving.
