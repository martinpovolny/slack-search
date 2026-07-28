package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/db"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

// CanvasInfo holds metadata about a discovered canvas.
type CanvasInfo struct {
	ChannelID   string
	ChannelName string
	FileID      string
	QuipID      string
	TabLabel    string
}

// DiscoverCanvases finds canvases attached to subscribed channels.
func DiscoverCanvases(conn *sql.DB, client *slackclient.Client) ([]CanvasInfo, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return nil, err
	}

	var results []CanvasInfo
	seenFileIDs := make(map[string]bool)

	for _, ch := range channels {
		raw, err := client.ConversationsInfo(ch.ID)
		if err != nil {
			log.Printf("  #%s: %v", ch.Name, err)
			continue
		}

		var resp struct {
			Channel struct {
				Properties struct {
					Canvas json.RawMessage `json:"canvas"`
					Tabs   json.RawMessage `json:"tabs"`
					Tabz   json.RawMessage `json:"tabz"`
				} `json:"properties"`
			} `json:"channel"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			log.Printf("  #%s: parse error: %v", ch.Name, err)
			continue
		}

		// Channel canvas
		if len(resp.Channel.Properties.Canvas) > 0 {
			var canvas struct {
				FileID       string `json:"file_id"`
				IsEmpty      bool   `json:"is_empty"`
				QuipThreadID string `json:"quip_thread_id"`
			}
			if json.Unmarshal(resp.Channel.Properties.Canvas, &canvas) == nil {
				if canvas.FileID != "" && !canvas.IsEmpty {
					seenFileIDs[canvas.FileID] = true
					results = append(results, CanvasInfo{
						ChannelID:   ch.ID,
						ChannelName: ch.Name,
						FileID:      canvas.FileID,
						QuipID:      canvas.QuipThreadID,
					})
				}
			}
		}

		// Canvas tabs — Slack uses both "tabs" and "tabz"
		for _, tabsRaw := range []json.RawMessage{resp.Channel.Properties.Tabs, resp.Channel.Properties.Tabz} {
			if len(tabsRaw) == 0 {
				continue
			}
			var tabs []struct {
				Type       string `json:"type"`
				IsDisabled bool   `json:"is_disabled"`
				Label      string `json:"label"`
				Data       struct {
					FileID string `json:"file_id"`
				} `json:"data"`
			}
			if json.Unmarshal(tabsRaw, &tabs) != nil {
				continue
			}
			for _, tab := range tabs {
				if (tab.Type == "canvas" || tab.Type == "channel_canvas") && !tab.IsDisabled {
					fid := tab.Data.FileID
					if fid == "" || seenFileIDs[fid] {
						continue
					}
					seenFileIDs[fid] = true
					results = append(results, CanvasInfo{
						ChannelID:   ch.ID,
						ChannelName: ch.Name,
						FileID:      fid,
						TabLabel:    tab.Label,
					})
				}
			}
		}
	}

	// Resolve quip IDs for any that are missing
	for i := range results {
		if results[i].QuipID != "" {
			continue
		}
		raw, err := client.QuipLookupThreadIds(results[i].FileID)
		if err != nil {
			log.Printf("  quip lookup for %s: %v", results[i].FileID, err)
			continue
		}
		var lookup struct {
			Lookup map[string]string `json:"lookup"`
		}
		if json.Unmarshal(raw, &lookup) == nil {
			results[i].QuipID = lookup.Lookup[results[i].FileID]
		}
	}

	return results, nil
}

// reTitleFilter matches strings that are unlikely to be good titles.
var reTitleFilter = regexp.MustCompile(`^[a-z_]+$`)

// DownloadCanvases discovers and downloads all canvases from subscribed channels.
// Returns the count of successfully downloaded canvases.
func DownloadCanvases(conn *sql.DB, client *slackclient.Client) (int, error) {
	canvases, err := DiscoverCanvases(conn, client)
	if err != nil {
		return 0, err
	}

	if len(canvases) == 0 {
		fmt.Println("No canvases found in subscribed channels.")
		return 0, nil
	}

	fmt.Printf("Found %d canvas(es) to download...\n", len(canvases))

	count := 0
	for _, c := range canvases {
		if c.QuipID == "" {
			fmt.Printf("  #%s: no quip ID, skipping\n", c.ChannelName)
			continue
		}

		fmt.Printf("  #%s (%s)... ", c.ChannelName, c.FileID)

		raw, err := client.FetchCanvas(c.QuipID)
		if err != nil {
			fmt.Printf("failed: %v\n", err)
			continue
		}

		plainText, htmlContent := slackclient.ExtractCanvasText(raw)

		// Determine title: prefer tab label, then first content line, then fallback
		title := strings.TrimSpace(c.TabLabel)
		if title == "" {
			for _, line := range strings.Split(plainText, "\n") {
				line = strings.TrimSpace(line)
				if len(line) > 5 && !strings.HasPrefix(line, "http") && !reTitleFilter.MatchString(line) {
					if len(line) > 100 {
						line = line[:100]
					}
					title = line
					break
				}
			}
		}
		if title == "" {
			title = fmt.Sprintf("Canvas (%s)", c.FileID[:8])
		}

		if err := db.InsertCanvas(conn, db.Canvas{
			FileID:      c.FileID,
			ChannelID:   c.ChannelID,
			QuipID:      c.QuipID,
			Title:       title,
			ContentText: plainText,
			ContentHTML:  htmlContent,
			UpdatedAt:   float64(time.Now().Unix()),
		}); err != nil {
			fmt.Printf("insert error: %v\n", err)
			continue
		}

		count++
		titleDisplay := title
		if len(titleDisplay) > 50 {
			titleDisplay = titleDisplay[:50]
		}
		fmt.Printf("OK %d chars, title: %s\n", len(plainText), titleDisplay)
	}

	fmt.Printf("Done. %d canvas(es) downloaded.\n", count)
	return count, nil
}
