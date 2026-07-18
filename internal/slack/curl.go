package slack

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// CurlCredentials holds credentials extracted from a Chrome "Copy as cURL" command.
type CurlCredentials struct {
	Token     string
	Cookie    string // value of the 'd' cookie (xoxd-…)
	RawCookies string // full Cookie header value
	Workspace string // e.g. redhat.enterprise.slack.com
	ChannelID string
}

var ansiCRe = regexp.MustCompile(`\$'((?:[^'\\]|\\.)*)'`)

func decodeAnsiC(content string) string {
	escapes := map[byte]byte{'n': '\n', 'r': '\r', 't': '\t', '\\': '\\', '\'': '\'', '"': '"'}
	var b strings.Builder
	for i := 0; i < len(content); i++ {
		if content[i] == '\\' && i+1 < len(content) {
			if r, ok := escapes[content[i+1]]; ok {
				b.WriteByte(r)
			} else {
				b.WriteByte(content[i+1])
			}
			i++
		} else {
			b.WriteByte(content[i])
		}
	}
	return b.String()
}

func expandAnsiCQuotes(text string) string {
	return ansiCRe.ReplaceAllStringFunc(text, func(m string) string {
		inner := ansiCRe.FindStringSubmatch(m)[1]
		decoded := decodeAnsiC(inner)
		escaped := strings.ReplaceAll(decoded, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		return `"` + escaped + `"`
	})
}

// shellSplit does a simple shell-like split handling single and double quotes.
func shellSplit(s string) ([]string, error) {
	var parts []string
	var current strings.Builder
	inSingle := false
	inDouble := false
	escaped := false

	for i := 0; i < len(s); i++ {
		ch := s[i]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if ch == '\\' && !inSingle {
			if inDouble {
				if i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\' || s[i+1] == '$') {
					escaped = true
					continue
				}
			} else {
				escaped = true
				continue
			}
		}
		if ch == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if ch == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if (ch == ' ' || ch == '\t' || ch == '\n') && !inSingle && !inDouble {
			if current.Len() > 0 {
				parts = append(parts, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(ch)
	}

	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote")
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts, nil
}

// ParseCurl extracts Slack credentials from a Chrome "Copy as cURL" command.
func ParseCurl(curlText string) (*CurlCredentials, error) {
	normalised := strings.ReplaceAll(curlText, "\\\n", " ")
	normalised = strings.ReplaceAll(normalised, "\\\r\n", " ")
	normalised = expandAnsiCQuotes(normalised)

	parts, err := shellSplit(normalised)
	if err != nil {
		return nil, fmt.Errorf("could not parse curl command: %w", err)
	}

	var rawURL string
	var cookieStr string
	var dataParts []string

	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "curl" {
			continue
		}
		if (p == "-H" || p == "--header") && i+1 < len(parts) {
			raw := parts[i+1]
			idx := strings.Index(raw, ":")
			if idx > 0 && strings.EqualFold(strings.TrimSpace(raw[:idx]), "cookie") {
				val := strings.TrimSpace(raw[idx+1:])
				if cookieStr != "" {
					cookieStr += "; " + val
				} else {
					cookieStr = val
				}
			}
			i++
			continue
		}
		if (p == "-b" || p == "--cookie") && i+1 < len(parts) {
			val := parts[i+1]
			if cookieStr != "" {
				cookieStr += "; " + val
			} else {
				cookieStr = val
			}
			i++
			continue
		}
		if (p == "--data-raw" || p == "--data-urlencode" || p == "--data" || p == "-d" || p == "-F") && i+1 < len(parts) {
			dataParts = append(dataParts, parts[i+1])
			i++
			continue
		}
		if !strings.HasPrefix(p, "-") && rawURL == "" {
			rawURL = p
		}
	}

	if rawURL == "" {
		return nil, fmt.Errorf("no URL found in curl command")
	}

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	workspace := parsedURL.Host
	channelID := parsedURL.Query().Get("channel")

	fullBody := strings.Join(dataParts, "\r\n")

	// Token from multipart body
	var token string
	multipartRe := regexp.MustCompile(`(?i)Content-Disposition:[^\r\n]*name=["']?token["']?[^\r\n]*\r\n\r\n([^\r\n]+)`)
	if m := multipartRe.FindStringSubmatch(fullBody); len(m) > 1 {
		token = strings.TrimSpace(m[1])
	}

	// Fallback: URL-encoded body
	if token == "" {
		tokenRe := regexp.MustCompile(`(?:^|&)token=([^&\s]+)`)
		if m := tokenRe.FindStringSubmatch(fullBody); len(m) > 1 {
			t, _ := url.QueryUnescape(m[1])
			token = t
		}
	}

	if token == "" {
		return nil, fmt.Errorf("could not find 'token' field in the curl body — make sure you copied a conversations.history request")
	}

	// d cookie
	var dCookie string
	dRe := regexp.MustCompile(`(?:^|;\s*)d=([^;]+)`)
	if m := dRe.FindStringSubmatch(cookieStr); len(m) > 1 {
		d, _ := url.QueryUnescape(strings.TrimSpace(m[1]))
		dCookie = d
	}

	// Channel from multipart body
	if channelID == "" {
		chRe := regexp.MustCompile(`(?i)Content-Disposition:[^\r\n]*name=["']?channel["']?[^\r\n]*\r\n\r\n([^\r\n]+)`)
		if m := chRe.FindStringSubmatch(fullBody); len(m) > 1 {
			channelID = strings.TrimSpace(m[1])
		}
	}

	creds := &CurlCredentials{
		Token:     token,
		Cookie:    dCookie,
		Workspace: workspace,
		ChannelID: channelID,
	}
	if cookieStr != "" {
		creds.RawCookies = cookieStr
	}
	return creds, nil
}
