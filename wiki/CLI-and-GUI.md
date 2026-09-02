# CLI and GUI

The same single executable can run the local dashboard or a terminal-only bot.
The GUI is a local control plane; it is not a hosted web service.

## Runtime flags

| Flag | Effect |
| --- | --- |
| `--gui` | Force-enable the dashboard. |
| `--no-gui` | Disable the dashboard. |
| `--gui-open` | Open the GUI window on startup. |
| `--no-gui-open` | Do not open a GUI window automatically. |
| `--gui-browser` | Open the dashboard in the system browser. |
| `--gui-auto-start` | Start the Discord runtime after the GUI is ready. |
| `--no-tray` | Disable system-tray behavior. |
| `--start-minimized` | Start the desktop window minimized. |
| `--gui-address host:port` | Select a loopback address and port. |
| `--config base` | Load `base.json`, `base.jsonc`, or `base.yaml`. |
| `base` | Legacy positional equivalent for the settings base. |

Both `--flag value` and the `--flag=value` form are accepted for `--config`
and `--gui-address`.

## Dashboard workflow

1. Launch the executable.
2. Wait for the configuration panel to load.
3. In normal mode, set the token, destination, IDs, and common download flags.
4. Use advanced mode to inspect less common fields such as filters, path
   overrides, source aliases, and history controls.
5. Save and review the validation result.
6. Press **Start application**.
7. Watch the activity table and application console.

The GUI can import/export JSON and save an atomic backup. It reloads settings
when the watcher detects a file change. Some start-time flags and process
layout changes still require a restart. A saved token is never returned as
plain text by the GUI API; a blank masked secret means “preserve the existing
value”.

## Security model

The server validates that its bind address is localhost or a loopback IP. Each
process generates a random GUI token, and API requests must carry it. The
dashboard uses restrictive response headers and limits configuration request
bodies. Keep the executable and settings directory private anyway: the local
API can edit the bot configuration for whoever can access the running process.

Do not change `guiAddress` to `0.0.0.0`, a LAN IP, or a public hostname. If a
remote operator is required, use a separate, authenticated remote-desktop or
VPN workflow and keep the application itself on loopback.

## Terminal-only operation

Use terminal-only mode for a server, scheduled task, or process manager:

```powershell
.\discord-downloader-go-gui.exe --no-gui --config server-archive
```

The process writes normal logs to the console and can optionally use
`logOutput`. Stop it with `Ctrl+C` or the `ddg exit` command when permitted.

## Layout beside the executable

```text
discord-downloader-go-gui.exe
settings.json
settings.json.bak             # created after GUI replacement saves
cache/
  history/                     # resumable history checkpoints
  settings.json / settings.yaml# conversion/cache copies when applicable
database/                      # normalized download records
backups/                       # optional dated database zip files
<archive destination>/         # downloaded files
```

The local maintainer-only `release/` bundle is ignored by Git. GitHub releases
contain one Windows amd64 executable; configuration and archives stay local.
