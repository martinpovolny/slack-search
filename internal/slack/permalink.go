package slack

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Permalink identifies a Slack message referenced by a permalink URL such as
// https://example.slack.com/archives/C04476G1F7H/p1795114772856229?thread_ts=1795092728.294809.
type Permalink struct {
	ChannelID string
	TS        string // the linked message
	ThreadTS  string // the thread parent, empty when the URL does not say
}

var permalinkPath = regexp.MustCompile(`/archives/([A-Z0-9]+)/p(\d{7,})$`)

// ParsePermalink extracts the channel ID and message timestamps from a Slack
// message permalink. The "p" path segment is the message ts without its dot;
// Slack always uses six digits after the dot.
func ParsePermalink(raw string) (Permalink, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Permalink{}, fmt.Errorf("parse permalink: %w", err)
	}
	m := permalinkPath.FindStringSubmatch(strings.TrimSuffix(u.Path, "/"))
	if m == nil {
		return Permalink{}, fmt.Errorf("not a Slack message permalink: %q", raw)
	}
	digits := m[2]
	link := Permalink{
		ChannelID: m[1],
		TS:        digits[:len(digits)-6] + "." + digits[len(digits)-6:],
		ThreadTS:  u.Query().Get("thread_ts"),
	}
	if cid := u.Query().Get("cid"); cid != "" && cid != link.ChannelID {
		return Permalink{}, fmt.Errorf("permalink channel %s does not match cid %s", link.ChannelID, cid)
	}
	return link, nil
}
