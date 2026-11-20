# Thread replies: how they are archived and when they can be missed

Slack's `conversations.history` returns only top-level messages ordered by
their own timestamp. A reply posted today to a parent from last week does not
make that parent reappear in an incremental history read. Thread replies are
therefore archived by separate `conversations.replies` calls, and which threads
get those calls decides whether a late reply is captured.

## The gap

An incremental refresh reads history newer than the channel's last stored
message. Before the history re-scan described below existed, the thread
catch-up only re-fetched parents whose cached `reply_count` was above zero.
A parent archived before its first reply was stored with `reply_count=0`, was
never returned by an incremental history read again, and was skipped by the
catch-up. Its whole thread stayed missing:

- `slack_thread` returned only the parent, or "No messages found";
- a permalinked reply was absent from `slack_grep` and `slack_sql`;
- `slack_refresh_channel` reported `new_thread_replies: 0`.

## What refresh does now

For each subscribed channel, refresh re-reads channel history over the larger
of the global lookback (`refresh --lookback N`, `serve --lookback N` with a
default of 7, the MCP `lookback_days`) and the channel's own `deep_scan_days`.
This updates every parent's authoritative `reply_count`, picks up edits to
top-level messages, and stores any message that is missing. Threads are then
fetched in two tiers by the age of their parent:

| Tier | Parents | Thread fetched when | Also captures |
|---|---|---|---|
| Deep | from the last `deep_scan_days` days | the parent has replies | edits to existing replies, refreshed files and blocks |
| Normal | older, within the lookback | Slack reports more replies, or a newer `latest_reply`, than the archive holds | — |

The normal tier is what recovers a parent archived before its first reply:
Slack reports replies that the archive lacks. Neither tier deletes messages
that were removed from Slack.

Cost per channel and refresh: one `conversations.history` call per 200
messages in the window, one `conversations.replies` call per thread in the
deep window, and one per thread with new replies in the normal window. With
the default one-second spacing between calls, keep deep windows short on busy
channels.

## Remaining limit

A reply to a parent older than the scan window is still not seen. Use one of:

- **One thread, now:** `slack-search fetch-thread <permalink>` or the MCP tool
  `slack_fetch_thread`. Both accept a permalink, or a channel plus the ts of
  the parent or any reply, and fetch the thread regardless of cached counts.
- **A longer window:** a `deep_scan_days` larger than the lookback also
  widens the history re-scan for that channel.

## Per-channel deep scan

```bash
slack-search channel-config --channel cost-mgmt-dev --deep-scan-days 3
slack-search channel-config            # list subscribed channels and windows
```

The MCP tool `slack_set_deep_scan` sets the same value. The window is stored
in `channels.deep_scan_days` (0 disables it, maximum 365).

## API call spacing

The Slack client waits at least one second between API calls by default. Set
another spacing with a Go duration string, for example `500ms`:

- environment variable `SLACK_SEARCH_API_INTERVAL`, which takes precedence; or
- `"api_min_interval": "500ms"` in `~/.slack-search/config.json`.

Allowed values range from `0s` to `1m`. Slack's HTTP 429 `Retry-After`
handling still applies, so a shorter spacing trades a faster refresh for
more rate-limit pauses.
