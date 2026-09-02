# History and Recovery Guide

History mode downloads older messages instead of waiting for new gateway
events. It is useful when a channel needs a first archive or when a previous
scan was interrupted.

## How a scan works

1. The bot resolves the requested channel, category, server, alias, or `all`.
2. Messages are fetched from Discord in pages of up to `historyRequestCount`.
3. Each selected attachment or link passes the same source and content filters
   used by real-time downloads.
4. Files are processed by the bounded history worker pool.
5. A checkpoint is promoted only after the selected page completes.

Completed unfiltered scans retain a per-channel high-water mark. Later scans
can stop at the newest message already covered instead of traversing the whole
archive again.

## Checkpoints

Active history checkpoints are stored under `cache/history/` as JSON files. A
checkpoint is safe to keep between restarts. If the process exits, the next
scan resumes from the last completed page rather than assuming that all queued
files finished.

Do not edit checkpoint files while a history job is running. To deliberately
start over, use `ddg history cachewipe` as an administrator or stop the
application and remove only the matching channel checkpoint.

## Cancellation and failures

Use `ddg history cancel` in the requesting channel, or use **Cancel** in the
GUI activity panel. Cancellation is cooperative: a request already being
written may finish before the job changes to its cancelled state.

For a failed job:

1. Read the job status and application log.
2. Check Discord permissions, rate limits, disk space, and the destination path.
3. Fix the configuration or environment issue.
4. Run the same history command again; the checkpoint will be reused.

The database records normalized source URLs per channel. Discord CDN signatures
are removed for lookup, so a refreshed signed URL does not normally cause a
second download.

## Large archives

Start with a single channel and a limited `--since`/`--before` range. Increase
`historyDownloadWorkers` only after confirming that Discord rate limits and
local disk throughput remain healthy. Keep `historyMaxJobs` low on small
machines and back up the `database/` directory before destructive operations.
