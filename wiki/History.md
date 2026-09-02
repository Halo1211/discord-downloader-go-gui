# History Archive

History mode downloads older messages instead of waiting for new gateway
events. A scan resolves its target, fetches message pages, applies the normal
source and file filters, and sends selected files through the bounded worker
queue.

## Checkpoints

Active checkpoints are stored under `cache/history/`. A checkpoint advances
only after a page completes, so an interrupted scan can resume safely. Do not
edit checkpoint files while a job is running.

Use the bot command or GUI action to cancel a job. Cancellation is cooperative:
a request already being written may finish before the job becomes cancelled.

## Recommended workflow

1. Start with one channel and a short `--since`/`--before` range.
2. Watch the GUI activity panel and application console.
3. Confirm disk usage and destination layout.
4. Expand the range only after the first batch is correct.
5. Back up `database/` before any destructive database operation.

For a failed job, check permissions, rate limits, disk space, destination
access, and the source rule. Re-running a scan reuses its checkpoint when
possible. Discord CDN signatures are normalized for deduplication.
