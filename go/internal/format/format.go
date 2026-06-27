package format

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

var mentionRe = regexp.MustCompile(`<@([A-Z0-9]+)(?:\|[^>]*)?>`)

// ExtractUIDs returns all user IDs mentioned in the texts.
func ExtractUIDs(texts []string) []string {
	seen := map[string]bool{}
	var uids []string
	for _, t := range texts {
		for _, m := range mentionRe.FindAllStringSubmatch(t, -1) {
			uid := m[1]
			if !seen[uid] {
				seen[uid] = true
				uids = append(uids, uid)
			}
		}
	}
	return uids
}

// BuildUserMap returns a map of user_id -> display name from the database.
func BuildUserMap(db *sql.DB, uids []string) map[string]string {
	if len(uids) == 0 {
		return nil
	}
	placeholders := make([]string, len(uids))
	args := make([]interface{}, len(uids))
	for i, uid := range uids {
		placeholders[i] = "?"
		args[i] = uid
	}

	query := fmt.Sprintf(
		"SELECT id, COALESCE(display_name, real_name, name, id) FROM users WHERE id IN (%s)",
		strings.Join(placeholders, ","),
	)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	m := make(map[string]string)
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			m[id] = name
		}
	}
	return m
}

// ResolveMentions replaces <@UXXXXXXX> with @Name in text.
func ResolveMentions(text string, userMap map[string]string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(match string) string {
		m := mentionRe.FindStringSubmatch(match)
		if len(m) > 1 {
			if name, ok := userMap[m[1]]; ok {
				return "@" + name
			}
		}
		return match
	})
}

// ResolveMentionsHTML replaces <@UXXXXXXX> with styled HTML spans.
func ResolveMentionsHTML(text string, userMap map[string]string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(match string) string {
		m := mentionRe.FindStringSubmatch(match)
		if len(m) > 1 {
			if name, ok := userMap[m[1]]; ok {
				return fmt.Sprintf(`<span style="color:#7c3aed;font-weight:600">@%s</span>`, name)
			}
		}
		return match
	})
}
