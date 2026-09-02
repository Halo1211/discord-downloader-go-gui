# Development

## Local checks

Go 1.24.1 or newer is recommended. From the repository root run:

```powershell
go test ./...
go vet ./...
go build ./...
```

The release workflow builds one Windows amd64 executable with CGO disabled
and publishes it when a `v*` tag is pushed. The maintainer's local `release/`
directory is intentionally ignored by Git.

## Documentation changes

Keep user-facing guides in English, use placeholders for all credentials, and
update `RELEASE_NOTES.md` when behavior or packaging changes. The `wiki/`
folder mirrors the original wiki navigation so it can be reviewed, versioned,
and shipped with source releases even when GitHub Wiki is unavailable.

## Repository references

The maintained implementation began from
[`get-got/discord-downloader-go`](https://github.com/get-got/discord-downloader-go).
Its original wiki was used as a documentation reference, but legacy
self-bot/user-token instructions were deliberately removed. Discord's current
[bot quick start](https://docs.discord.com/developers/quick-start/getting-started),
[OAuth2 and permissions guide](https://docs.discord.com/developers/platform/oauth2-and-permissions),
and [Gateway reference](https://docs.discord.com/developers/events/gateway) are
the authoritative setup references.

When changing a configuration field, update `settings.example.json`, the
relevant wiki page, and the GUI's normal/advanced labels if the field is user
facing. Keep examples valid JSON and use obvious placeholders for IDs,
credentials, and filesystem paths.
