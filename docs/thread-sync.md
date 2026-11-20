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

For each subscribed channel, the scan window is the larger of the global
lookback (`refresh --lookback N`, `serve --lookback N`, the MCP
`lookback_days`) and the channel's own `deep_scan_days`. Within that window,
refresh:

1. re-reads channel history, which updates every parent's authoritative
   `reply_count` and stores any message that is missing; and
2. re-fetches every thread with replies whose parent is in the window. This
   captures new replies and edits to existing replies.

Cost per channel and refresh: one `conversations.history` call per 200
messages in the window plus one `conversations.replies` call per thread. With
the default one-second spacing between calls, a busy channel with a long
window adds noticeable time to each refresh.

## Remaining limit

A reply to a parent older than the scan window is still not seen. Use one of:

- **One thread, now:** `slack-search fetch-thread <permalink>` or the MCP tool
  `slack_fetch_thread`. Both accept a permalink, or a channel plus the ts of
  the parent or any reply, and fetch the thread regardless of cached counts.
- **Important channels, always:** give the channel a longer deep-scan window:

  ```bash
  slack-search channel-config --channel cost-mgmt-dev --deep-scan-days 30
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
