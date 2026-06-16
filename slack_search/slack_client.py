"""
Thin Slack API client that sends the token as a POST form field.

The official slack_sdk.WebClient puts the token in Authorization: Bearer,
which Enterprise Slack rejects for browser-session xoxc- tokens.
This client replicates what Chrome does: POST with token= in the form body
and the xoxd- session cookie in the Cookie header.
"""

import time
from typing import Any, Optional

import requests

MIN_INTERVAL = 1.0  # seconds between API calls


class SlackClient:
    def __init__(
        self,
        token: str,
        cookie: Optional[str] = None,
        workspace: Optional[str] = None,
        raw_cookies: Optional[str] = None,
    ) -> None:
        self.token = token
        base = workspace or "slack.com"
        self.base_url = f"https://{base}/api"
        self.session = requests.Session()
        self.session.headers.update({"User-Agent": "Mozilla/5.0"})
        # raw_cookies (from --curl) takes priority — Enterprise Slack needs all session cookies
        if raw_cookies:
            self.session.headers["Cookie"] = raw_cookies
        elif cookie:
            self.session.headers["Cookie"] = f"d={cookie}"
        self._last_call: float = 0.0

    def _throttle(self) -> None:
        elapsed = time.monotonic() - self._last_call
        if elapsed < MIN_INTERVAL:
            time.sleep(MIN_INTERVAL - elapsed)
        self._last_call = time.monotonic()

    def _post(self, method: str, **params: Any) -> dict:
        for attempt in range(5):
            self._throttle()
            resp = self.session.post(
                f"{self.base_url}/{method}",
                data={"token": self.token, **params},
                timeout=30,
            )
            resp.raise_for_status()
            data = resp.json()
            if data.get("ok"):
                return data
            error = data.get("error", "unknown_error")
            if error == "ratelimited":
                retry_after = int(resp.headers.get("Retry-After", 30))
                from rich.console import Console
                Console().print(f"[yellow]Rate limited — waiting {retry_after}s…[/]")
                time.sleep(retry_after)
                continue
            raise RuntimeError(f"Slack API error from {method}: {error}")
        raise RuntimeError(f"Exceeded retry limit for {method}")

    def conversations_info(self, channel: str) -> dict:
        return self._post("conversations.info", channel=channel)

    def conversations_list(self, **kw: Any) -> dict:
        return self._post("conversations.list", **kw)

    def conversations_history(self, **kw: Any) -> dict:
        return self._post("conversations.history", **kw)

    def conversations_replies(self, **kw: Any) -> dict:
        return self._post("conversations.replies", **kw)

    def users_info(self, user: str) -> dict:
        return self._post("users.info", user=user)

    def search_messages(self, query: str, count: int = 50, page: int = 1, **kw: Any) -> dict:
        return self._post("search.messages", query=query, count=count, page=page, **kw)
