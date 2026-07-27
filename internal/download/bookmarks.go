package download

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/martinpovolny/slack-search/internal/db"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

// DownloadBookmarks fetches bookmarks from all subscribed channels and stores them.
func DownloadBookmarks(conn *sql.DB, client *slackclient.Client) (int, error) {
	channels, err := db.SubscribedChannels(conn)
	if err != nil {
		return 0, err
	}

	total := 0
	for _, ch := range channels {
		raw, err := client.BookmarksList(ch.ID)
		if err != nil {
			log.Printf("  #%s: %v", ch.Name, err)
			continue
		}

		var resp struct {
			OK        bool `json:"ok"`
			Bookmarks []struct {
				ID          string  `json:"id"`
				Title       string  `json:"title"`
				Link        string  `json:"link"`
				Type        string  `json:"type"`
				Emoji       string  `json:"emoji"`
				DateCreated float64 `json:"date_created"`
			} `json:"bookmarks"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			log.Printf("  #%s: parse error: %v", ch.Name, err)
			continue
		}

		count := 0
		for _, b := range resp.Bookmarks {
			if b.ID == "" {
				continue
			}
			if err := db.InsertBookmark(conn, db.Bookmark{
				ID:        b.ID,
				ChannelID: ch.ID,
				Title:     b.Title,
				Link:      b.Link,
				Type:      b.Type,
				Emoji:     b.Emoji,
				CreatedAt: b.DateCreated,
			}); err != nil {
				log.Printf("  #%s: insert error: %v", ch.Name, err)
				continue
			}
			count++
		}
		if count > 0 {
			fmt.Printf("  #%s: %d bookmark(s)\n", ch.Name, count)
			total += count
		}
	}
	fmt.Printf("Done. %d bookmark(s) across %d channel(s).\n", total, len(channels))
	return total, nil
}
