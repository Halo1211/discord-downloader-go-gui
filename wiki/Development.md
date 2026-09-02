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
