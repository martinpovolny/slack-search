"""Parse a curl command copy-pasted from Chrome DevTools → Copy as cURL."""

import re
import shlex
import urllib.parse
from dataclasses import dataclass
from typing import Optional


@dataclass
class CurlCredentials:
    token: str
    cookie: Optional[str]       # value of the 'd' cookie (xoxd-…)
    raw_cookies: Optional[str]  # full Cookie header value from -b (needed for xoxc- auth)
    workspace: str              # e.g. redhat.enterprise.slack.com
    channel_id: Optional[str]


# ── ANSI-C $'...' quote expansion ──────────────────────────────────────────

_ANSI_C = re.compile(r"\$'((?:[^'\\]|\\.)*)'")
_ESCAPES = {'n': '\n', 'r': '\r', 't': '\t', '\\': '\\', "'": "'", '"': '"'}


def _decode_ansi_c(content: str) -> str:
    result = []
    i = 0
    while i < len(content):
        if content[i] == '\\' and i + 1 < len(content):
            result.append(_ESCAPES.get(content[i + 1], content[i + 1]))
            i += 2
        else:
            result.append(content[i])
            i += 1
    return ''.join(result)


def _expand_ansi_c_quotes(text: str) -> str:
    """Replace $'...' shell quoting with double-quoted equivalents for shlex."""
    def replace(m: re.Match) -> str:
        decoded = _decode_ansi_c(m.group(1))
        escaped = decoded.replace('\\', '\\\\').replace('"', '\\"')
        return f'"{escaped}"'
    return _ANSI_C.sub(replace, text)


# ── Main parser ─────────────────────────────────────────────────────────────

def parse_curl(curl_text: str) -> CurlCredentials:
    """
    Extract Slack credentials from a 'Copy as cURL' string from Chrome DevTools.

    Handles:
    - $'...' ANSI-C quoting used by Chrome for --data-raw with \\r\\n
    - token in multipart form-data POST body
    - xoxd- session cookie in the -b / Cookie header
    - workspace hostname from the request URL
    """
    # Normalise line continuations, then expand $'...' before shlex sees it
    normalised = curl_text.replace('\\\n', ' ').replace('\\\r\n', ' ')
    normalised = _expand_ansi_c_quotes(normalised)

    try:
        parts = shlex.split(normalised)
    except ValueError as e:
        raise ValueError(f"Could not parse curl command: {e}") from e

    url = None
    cookie_str = ''
    data_parts: list[str] = []

    i = 0
    while i < len(parts):
        p = parts[i]
        if p == 'curl':
            i += 1
            continue
        if p in ('-H', '--header') and i + 1 < len(parts):
            raw = parts[i + 1]
            key, _, val = raw.partition(':')
            if key.strip().lower() == 'cookie':
                cookie_str = cookie_str + '; ' + val.strip() if cookie_str else val.strip()
            i += 2
            continue
        if p in ('-b', '--cookie') and i + 1 < len(parts):
            s = parts[i + 1]
            cookie_str = cookie_str + '; ' + s if cookie_str else s
            i += 2
            continue
        if p in ('--data-raw', '--data-urlencode', '--data', '-d', '-F') and i + 1 < len(parts):
            data_parts.append(parts[i + 1])
            i += 2
            continue
        if not p.startswith('-') and url is None:
            url = p
        i += 1

    if url is None:
        raise ValueError("No URL found in curl command.")

    parsed_url = urllib.parse.urlparse(url)
    workspace = parsed_url.netloc

    # Channel may be in the query string
    qs = urllib.parse.parse_qs(parsed_url.query)
    channel_id = qs.get('channel', [None])[0]

    full_body = '\r\n'.join(data_parts)

    # --- token from multipart body ---
    # Pattern: Content-Disposition: …; name="token"\r\n\r\n<value>\r\n
    token = None
    m = re.search(
        r'Content-Disposition:[^\r\n]*name=["\']?token["\']?[^\r\n]*\r\n\r\n([^\r\n]+)',
        full_body,
        re.IGNORECASE,
    )
    if m:
        token = m.group(1).strip()

    # Fallback: url-encoded body
    if not token:
        m = re.search(r'(?:^|&)token=([^&\s]+)', full_body)
        if m:
            token = urllib.parse.unquote_plus(m.group(1))

    if not token:
        raise ValueError(
            "Could not find 'token' field in the curl body.\n"
            "Make sure you copied a request to …/api/conversations.history "
            "that contains the token in the Payload tab."
        )

    # --- d cookie ---
    d_cookie = None
    m = re.search(r'(?:^|;\s*)d=([^;]+)', cookie_str)
    if m:
        d_cookie = urllib.parse.unquote(m.group(1).strip())

    # Channel may also appear in multipart body
    if not channel_id:
        m = re.search(
            r'Content-Disposition:[^\r\n]*name=["\']?channel["\']?[^\r\n]*\r\n\r\n([^\r\n]+)',
            full_body,
            re.IGNORECASE,
        )
        if m:
            channel_id = m.group(1).strip()

    return CurlCredentials(
        token=token,
        cookie=d_cookie,
        raw_cookies=cookie_str or None,
        workspace=workspace,
        channel_id=channel_id,
    )
