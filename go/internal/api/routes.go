package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/martinpovolny/slack-search/internal/db"
	"github.com/martinpovolny/slack-search/internal/nlq"
	"github.com/martinpovolny/slack-search/internal/search"
)

type Handler struct {
	db  *sql.DB
	mux *http.ServeMux
}

func NewHandler(database *sql.DB) *Handler {
	h := &Handler{db: database}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/channels", h.handleChannels)
	mux.HandleFunc("/api/search", h.handleSearch)
	mux.HandleFunc("/api/schema", h.handleSchema)
	mux.HandleFunc("/api/grep", h.handleGrep)
	mux.HandleFunc("/api/nlq", h.handleNLQ)
	mux.HandleFunc("/api/messages", h.handleMessages)
	mux.HandleFunc("/api/stats", h.handleStats)
	h.mux = mux
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	h.mux.ServeHTTP(w, r)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) handleChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := db.AllChannels(h.db)
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
		Question string `json:"question"`
		Model    string `json:"model"`
		MaxRows  int    `json:"max_rows"`
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
		for k := range config.Models {
			modelName = k
			break
		}
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

	result, qErr := nlq.RunQuery(h.db, req.Question, baseURL, apiKey, apiModelID, maxRows)
	if qErr != nil && result == nil {
		jsonError(w, qErr.Error(), 500)
		return
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

	opts := search.GrepOptions{
		Limit: limit,
		Person: person,
		Since:  since,
		Until:  until,
	}
	if len(channel) > 0 {
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
	})
}
