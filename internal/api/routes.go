package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/martinpovolny/slack-search/internal/config"
	"github.com/martinpovolny/slack-search/internal/db"
	"github.com/martinpovolny/slack-search/internal/nlq"
	"github.com/martinpovolny/slack-search/internal/search"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
)

var startTime = time.Now()

// Injected at build time via main.
var (
	Commit    = "dev"
	BuildTime = "unknown"
)

type Handler struct {
	db          *sql.DB
	convDB      *sql.DB
	slackClient *slackclient.Client
	dbPath      string
	workspace   string
	mux         *http.ServeMux
}

func NewHandler(database *sql.DB, convDB *sql.DB, slackClient *slackclient.Client, dbPath, workspace string) *Handler {
	h := &Handler{db: database, convDB: convDB, slackClient: slackClient, dbPath: dbPath, workspace: workspace}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/channels", h.handleChannels)
	mux.HandleFunc("/api/search", h.handleSearch)
	mux.HandleFunc("/api/schema", h.handleSchema)
	mux.HandleFunc("/api/grep", h.handleGrep)
	mux.HandleFunc("/api/nlq", h.handleNLQ)
	mux.HandleFunc("/api/messages", h.handleMessages)
	mux.HandleFunc("/api/stats", h.handleStats)
	mux.HandleFunc("/api/conversations", h.handleConversations)
	mux.HandleFunc("/api/conversations/", h.handleConversation)
	mux.HandleFunc("/api/slack-search", h.handleSlackSearch)
	mux.HandleFunc("/api/slack-status", h.handleSlackStatus)
	mux.HandleFunc("/api/runtime", h.handleRuntime)
	mux.HandleFunc("/api/config", h.handleConfig)
	h.mux = mux
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	h.mux.ServeHTTP(w, r)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) handleChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := db.AllChannelsWithSubscribed(h.db)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(channels)
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonError(w, "missing 'q' parameter", 400)
		return
	}
	result, err := search.RunSQL(h.db, query)
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	json.NewEncoder(w).Encode(result)
}

func (h *Handler) handleSchema(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"schema": search.SchemaDescription()})
}

func (h *Handler) handleGrep(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 200
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	results, err := search.Grep(h.db, search.GrepOptions{
		FixedString: q.Get("string"),
		Pattern:     q.Get("pattern"),
		Channels:    q["channel"],
		Since:       q.Get("since"),
		Until:       q.Get("until"),
		Person:      q.Get("person"),
		Limit:       limit,
	})
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	json.NewEncoder(w).Encode(results)
}

func (h *Handler) handleNLQ(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonError(w, "POST required", 405)
		return
	}
	var req struct {
		Question       string `json:"question"`
		Model          string `json:"model"`
		MaxRows        int    `json:"max_rows"`
		ConversationID string `json:"conversation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", 400)
		return
	}
	if req.Question == "" {
		jsonError(w, "missing 'question'", 400)
		return
	}

	config, err := nlq.LoadRHTConfig()
	if err != nil {
		jsonError(w, fmt.Sprintf("LLM config error: %v", err), 500)
		return
	}
	modelName := req.Model
	if modelName == "" {
		modelName = config.DefaultModelName()
	}
	baseURL, apiKey, apiModelID, err := config.Endpoint(modelName)
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	maxRows := req.MaxRows
	if maxRows <= 0 {
		maxRows = nlq.DefaultMaxRows
	}

	// Save user message to conversation
	if req.ConversationID != "" && h.convDB != nil {
		db.AppendConvMessage(h.convDB, req.ConversationID, "user", req.Question, "", "", "")
	}

	queryStart := time.Now()
	result, qErr := nlq.RunQuery(h.db, req.Question, baseURL, apiKey, apiModelID, maxRows)
	duration := time.Since(queryStart)
	if qErr != nil && result == nil {
		jsonError(w, qErr.Error(), 500)
		return
	}

	// Save assistant response to conversation
	if req.ConversationID != "" && h.convDB != nil && result != nil {
		content := result.Answer
		if content == "" && result.SQL != "" {
			content = "SQL: " + result.SQL
		}
		sqlText := result.SQL
		var resultJSON string
		if result.Result != nil {
			if data, err := json.Marshal(result.Result); err == nil {
				resultJSON = string(data)
			}
		}
		meta := map[string]interface{}{
			"model":       modelName,
			"api_model":   apiModelID,
			"duration_ms": duration.Milliseconds(),
			"mode":        result.Mode,
			"max_rows":    maxRows,
		}
		if result.Error != "" {
			meta["error"] = result.Error
		}
		metaJSON, _ := json.Marshal(meta)
		db.AppendConvMessage(h.convDB, req.ConversationID, "assistant", content, sqlText, resultJSON, string(metaJSON))

		// Auto-title on first exchange — ask the LLM for a short title
		convMsgs, _ := db.LoadConvMessages(h.convDB, req.ConversationID)
		if len(convMsgs) <= 2 {
			titlePrompt := "You generate short conversation titles based on the user's question. The title must reflect what the user asked, not what the answer contained. Reply with ONLY the title — no quotes, no punctuation at the end, 5 words maximum."
			titleQ := fmt.Sprintf("Question: %s", req.Question)
			if title, err := nlq.ChatComplete(baseURL, apiKey, apiModelID, titlePrompt, titleQ); err == nil {
				title = strings.TrimSpace(title)
				if len(title) > 0 && len(title) < 80 {
					db.RenameConversation(h.convDB, req.ConversationID, title)
				}
			}
		}
	}

	json.NewEncoder(w).Encode(result)
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 25
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	channel := q.Get("channel")
	person := q.Get("person")
	text := q.Get("text")
	since := q.Get("since")
	until := q.Get("until")
	useRegexp := q.Get("regexp") == "true"

	opts := search.GrepOptions{Limit: limit, Person: person, Since: since, Until: until}
	if channel != "" {
		opts.Channels = []string{channel}
	}
	if useRegexp {
		opts.Pattern = text
	} else if text != "" {
		opts.FixedString = text
	}

	results, err := search.Grep(h.db, opts)
	if err != nil {
		jsonError(w, err.Error(), 400)
		return
	}
	json.NewEncoder(w).Encode(results)
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	var msgCount, chCount int
	h.db.QueryRow("SELECT count(*) FROM messages").Scan(&msgCount)
	h.db.QueryRow("SELECT count(*) FROM channels").Scan(&chCount)

	var oldest, newest sql.NullString
	h.db.QueryRow(
		"SELECT date(min(timestamp),'unixepoch'), date(max(timestamp),'unixepoch') FROM messages",
	).Scan(&oldest, &newest)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"message_count": msgCount,
		"channel_count": chCount,
		"oldest":        oldest.String,
		"newest":        newest.String,
		"workspace":     h.workspace,
	})
}

// Conversation endpoints

func (h *Handler) handleConversations(w http.ResponseWriter, r *http.Request) {
	if h.convDB == nil {
		jsonError(w, "conversations not configured", 500)
		return
	}
	switch r.Method {
	case "GET":
		convs, err := db.ListConversations(h.convDB)
		if err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		if convs == nil {
			convs = []db.Conversation{}
		}
		json.NewEncoder(w).Encode(convs)
	case "POST":
		id, err := db.CreateConversation(h.convDB)
		if err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": id})
	default:
		jsonError(w, "method not allowed", 405)
	}
}

func (h *Handler) handleConversation(w http.ResponseWriter, r *http.Request) {
	if h.convDB == nil {
		jsonError(w, "conversations not configured", 500)
		return
	}
	// Extract conversation ID from path: /api/conversations/{id}
	id := strings.TrimPrefix(r.URL.Path, "/api/conversations/")
	if id == "" {
		jsonError(w, "missing conversation id", 400)
		return
	}

	// Handle /api/conversations/{id}/messages
	if strings.HasSuffix(id, "/messages") {
		id = strings.TrimSuffix(id, "/messages")
		msgs, err := db.LoadConvMessages(h.convDB, id)
		if err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		if msgs == nil {
			msgs = []db.ConvMessage{}
		}
		json.NewEncoder(w).Encode(msgs)
		return
	}

	switch r.Method {
	case "DELETE":
		if err := db.DeleteConversation(h.convDB, id); err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
	case "PATCH":
		var req struct {
			Title string `json:"title"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Title == "" {
			jsonError(w, "missing title", 400)
			return
		}
		if err := db.RenameConversation(h.convDB, id, req.Title); err != nil {
			jsonError(w, err.Error(), 500)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "renamed"})
	default:
		jsonError(w, "method not allowed", 405)
	}
}

// Slack live search

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Load()
	json.NewEncoder(w).Encode(cfg)
}

func (h *Handler) handleSlackStatus(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]bool{"connected": h.slackClient != nil})
}

func (h *Handler) handleRuntime(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	uptime := time.Since(startTime)

	var dbSize int64
	if fi, err := os.Stat(h.dbPath); err == nil {
		dbSize = fi.Size()
	}

	// Last refresh: oldest latest_ts across subscribed channels
	var lastRefresh sql.NullString
	h.db.QueryRow(`
		SELECT datetime(min(CAST(ds.latest_ts AS REAL)), 'unixepoch')
		FROM download_state ds
		JOIN channels c ON ds.channel_id = c.id
		WHERE c.subscribed = 1
	`).Scan(&lastRefresh)

	json.NewEncoder(w).Encode(map[string]interface{}{
		"commit":       Commit,
		"build_time":   BuildTime,
		"go_version":   runtime.Version(),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"uptime_sec":   int(uptime.Seconds()),
		"goroutines":   runtime.NumGoroutine(),
		"alloc_mb":     float64(mem.Alloc) / 1024 / 1024,
		"sys_mb":       float64(mem.Sys) / 1024 / 1024,
		"gc_cycles":    mem.NumGC,
		"heap_objects": mem.HeapObjects,
		"db_size_mb":   float64(dbSize) / 1024 / 1024,
		"last_refresh": lastRefresh.String,
	})
}

func (h *Handler) handleSlackSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		jsonError(w, "POST required", 405)
		return
	}
	if h.slackClient == nil {
		jsonError(w, "Slack credentials not loaded. Start the server with --curl-file .curl", 400)
		return
	}
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body", 400)
		return
	}
	if req.Query == "" {
		jsonError(w, "missing query", 400)
		return
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}

	results, err := slackclient.LiveSearch(h.db, h.slackClient, req.Query, req.Limit)
	if err != nil {
		if slackclient.IsAuthError(err) {
			jsonError(w, "Slack credentials expired. Re-copy .curl from Chrome DevTools.", 401)
			return
		}
		jsonError(w, err.Error(), 500)
		return
	}
	if results == nil {
		results = []slackclient.SearchResult{}
	}
	json.NewEncoder(w).Encode(results)
}
