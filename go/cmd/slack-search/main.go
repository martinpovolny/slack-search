package main

import (
	"database/sql"
	"flag"
	"fmt"
	iofs "io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/martinpovolny/slack-search/internal/api"
	"github.com/martinpovolny/slack-search/internal/db"
	"github.com/martinpovolny/slack-search/internal/download"
	"github.com/martinpovolny/slack-search/internal/eval"
	"github.com/martinpovolny/slack-search/internal/nlq"
	"github.com/martinpovolny/slack-search/internal/search"
	slackclient "github.com/martinpovolny/slack-search/internal/slack"
	"github.com/martinpovolny/slack-search/internal/web"
)

var (
	commit    = "dev"
	buildTime = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	dbPath := defaultDBPath()

	switch os.Args[1] {
	case "download":
		cmdDownload(dbPath)
	case "refresh":
		cmdRefresh(dbPath)
	case "search":
		cmdSearch(dbPath)
	case "schema":
		cmdSchema()
	case "nlq":
		cmdNLQ(dbPath)
	case "grep":
		cmdGrep(dbPath)
	case "eval":
		cmdEval(dbPath)
	case "serve":
		cmdServe(dbPath)
	case "version":
		fmt.Printf("slack-search %s (built %s)\n", commit, buildTime)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`slack-search — Local Slack Archive with SQL and Natural Language Search

Commands:
  download    Download messages from a Slack channel
  refresh     Refresh all subscribed channels
  search      Run a raw SQL query
  schema      Show the database schema
  nlq         Natural language query (NL → SQL)
  grep        Search messages by text or regex
  eval        Run NLQ evaluation test suite
  serve       Start the web UI server

  version     Show version info
  help        Show this help

Data directory: ~/.slack-search/`)
}

func defaultDBPath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".slack-search")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, "messages.db")
}

func openDB(path string) *sql.DB {
	conn, err := db.Open(path)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	return conn
}

// parseCredentials extracts Slack credentials from flags or a curl file.
func parseCredentials(fs *flag.FlagSet) (token, cookie, workspace, rawCookies, channelHint string) {
	var curlFile, tokenFlag, cookieFlag, workspaceFlag string
	fs.StringVar(&curlFile, "curl-file", "", "Path to file containing a Chrome 'Copy as cURL' command (e.g. .curl)")
	fs.StringVar(&tokenFlag, "token", os.Getenv("SLACK_TOKEN"), "Slack token (xoxp-/xoxb-/xoxc-)")
	fs.StringVar(&cookieFlag, "cookie", os.Getenv("SLACK_COOKIE"), "Session cookie value (xoxc- only)")
	fs.StringVar(&workspaceFlag, "workspace", os.Getenv("SLACK_WORKSPACE"), "Workspace hostname")
	fs.Parse(os.Args[2:])

	if curlFile != "" {
		data, err := os.ReadFile(curlFile)
		if err != nil {
			log.Fatalf("Cannot read curl file %s: %v", curlFile, err)
		}
		creds, err := slackclient.ParseCurl(string(data))
		if err != nil {
			log.Fatalf("Cannot parse curl: %v", err)
		}
		if tokenFlag == "" {
			tokenFlag = creds.Token
		}
		if cookieFlag == "" {
			cookieFlag = creds.Cookie
		}
		if workspaceFlag == "" {
			workspaceFlag = creds.Workspace
		}
		rawCookies = creds.RawCookies
		channelHint = creds.ChannelID
	}

	if tokenFlag == "" {
		log.Fatal("No token found. Use --token, set SLACK_TOKEN, or use --curl-file")
	}

	return tokenFlag, cookieFlag, workspaceFlag, rawCookies, channelHint
}

func cmdDownload(dbPath string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	var channel, since string
	var noThreads bool
	fs.StringVar(&channel, "channel", "", "Channel name or ID (required)")
	fs.StringVar(&since, "since", "", "Fetch messages after this Unix timestamp or date")
	fs.BoolVar(&noThreads, "no-threads", false, "Skip fetching thread replies")

	// parseCredentials will parse the rest
	token, cookie, workspace, rawCookies, channelHint := parseCredentials(fs)

	if channel == "" && channelHint != "" {
		channel = channelHint
	}
	if channel == "" {
		log.Fatal("--channel is required")
	}

	conn := openDB(dbPath)
	defer conn.Close()

	client := slackclient.NewClient(token, cookie, workspace, rawCookies)

	channelID, channelName, err := download.ResolveChannel(client, channel, conn, channelHint)
	if err != nil {
		log.Fatalf("Cannot resolve channel: %v", err)
	}

	count, err := download.Download(conn, client, channelID, channelName, download.Options{
		FetchThreads: !noThreads,
		Since:        since,
	})
	if err != nil {
		if slackclient.IsAuthError(err) {
			fmt.Fprintf(os.Stderr, "\nAuthentication failed: %v\n", err)
			os.Exit(2)
		}
		log.Fatal(err)
	}
	fmt.Printf("Done. %d new message(s) stored.\n", count)
}

func cmdRefresh(dbPath string) {
	fs := flag.NewFlagSet("refresh", flag.ExitOnError)
	var noThreads bool
	fs.BoolVar(&noThreads, "no-threads", false, "Skip fetching thread replies")

	token, cookie, workspace, rawCookies, _ := parseCredentials(fs)

	conn := openDB(dbPath)
	defer conn.Close()

	client := slackclient.NewClient(token, cookie, workspace, rawCookies)

	_, err := download.Refresh(conn, client, download.Options{
		FetchThreads: !noThreads,
	})
	if err != nil {
		if slackclient.IsAuthError(err) {
			fmt.Fprintf(os.Stderr, "\nAuthentication failed: %v\n", err)
			os.Exit(2)
		}
		log.Fatal(err)
	}
}

func cmdSearch(dbPath string) {
	if len(os.Args) < 3 {
		log.Fatal("Usage: slack-search search \"SELECT ...\"")
	}
	query := os.Args[2]

	conn := openDB(dbPath)
	defer conn.Close()

	result, err := search.RunSQL(conn, query)
	if err != nil {
		log.Fatalf("SQL error: %v", err)
	}

	printTable(result)
}

func cmdSchema() {
	fmt.Println(search.SchemaDescription())
}

func cmdNLQ(dbPath string) {
	fs := flag.NewFlagSet("nlq", flag.ExitOnError)
	var modelName string
	var maxRows int
	fs.StringVar(&modelName, "model", "", "RHT model name from .rht_models.json")
	fs.IntVar(&maxRows, "max-rows", nlq.DefaultMaxRows, "Max rows sent to LLM for synthesis")
	fs.Parse(os.Args[2:])

	if fs.NArg() == 0 {
		log.Fatal("Usage: slack-search nlq [--model NAME] \"your question\"")
	}
	question := strings.Join(fs.Args(), " ")

	config, err := nlq.LoadRHTConfig()
	if err != nil {
		log.Fatalf("Cannot load LLM config: %v", err)
	}

	if modelName == "" {
		// Use first available model
		for k := range config.Models {
			modelName = k
			break
		}
	}

	baseURL, apiKey, apiModelID, err := config.Endpoint(modelName)
	if err != nil {
		log.Fatal(err)
	}

	conn := openDB(dbPath)
	defer conn.Close()

	fmt.Printf("Asking: %s\n", question)
	fmt.Printf("Model: %s\n\n", modelName)

	result, err := nlq.RunQuery(conn, question, baseURL, apiKey, apiModelID, maxRows)
	if err != nil {
		log.Fatal(err)
	}

	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", result.Error)
	}
	if result.SQL != "" {
		fmt.Printf("SQL:\n  %s\n\n", result.SQL)
	}
	if result.Result != nil {
		printTable(result.Result)
	}
	if result.Answer != "" {
		fmt.Printf("\n%s\n", result.Answer)
	}
}

func cmdGrep(dbPath string) {
	fs := flag.NewFlagSet("grep", flag.ExitOnError)
	var fixedStr, pattern, person, since, until string
	var channels stringSlice
	var limit int
	fs.StringVar(&fixedStr, "F", "", "Fixed string search (case-insensitive)")
	fs.StringVar(&pattern, "E", "", "Regex pattern search (case-insensitive)")
	fs.Var(&channels, "c", "Channel name or ID (repeatable)")
	fs.StringVar(&since, "since", "", "Since timestamp or date")
	fs.StringVar(&until, "until", "", "Until timestamp or date")
	fs.StringVar(&person, "p", "", "Person name (partial match)")
	fs.IntVar(&limit, "n", 200, "Max results")
	fs.Parse(os.Args[2:])

	if fixedStr == "" && pattern == "" {
		log.Fatal("Usage: slack-search grep -F \"string\" or -E \"regex\"")
	}

	conn := openDB(dbPath)
	defer conn.Close()

	results, err := search.Grep(conn, search.GrepOptions{
		FixedString: fixedStr,
		Pattern:     pattern,
		Channels:    channels,
		Since:       since,
		Until:       until,
		Person:      person,
		Limit:       limit,
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, r := range results {
		prefix := ""
		if r.ThreadTS != "" && r.ThreadTS != r.TS {
			prefix = "↳ "
		}
		fmt.Printf("[%s] #%s %s: %s%s\n", r.Time, r.Channel, r.Author, prefix, r.Text)
	}
	fmt.Printf("\n%d result(s)\n", len(results))
}

func cmdEval(dbPath string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	var modelName, testDir, resultsDir string
	fs.StringVar(&modelName, "model", "", "RHT model name")
	fs.StringVar(&testDir, "tests", "tests", "Directory with test case JSON files")
	fs.StringVar(&resultsDir, "results", "tests/results", "Directory for result output")
	fs.Parse(os.Args[2:])

	config, err := nlq.LoadRHTConfig()
	if err != nil {
		log.Fatalf("Cannot load LLM config: %v", err)
	}
	if modelName == "" {
		for k := range config.Models {
			modelName = k
			break
		}
	}
	baseURL, apiKey, apiModelID, err := config.Endpoint(modelName)
	if err != nil {
		log.Fatal(err)
	}

	tests, err := eval.LoadTests(testDir)
	if err != nil {
		log.Fatalf("Cannot load tests: %v", err)
	}
	if len(tests) == 0 {
		fmt.Printf("No test cases found in %s\n", testDir)
		return
	}

	conn := openDB(dbPath)
	defer conn.Close()

	fmt.Printf("Running %d tests with model %s…\n\n", len(tests), modelName)
	results := eval.RunEval(conn, tests, baseURL, apiKey, apiModelID)

	fmt.Println()
	eval.PrintSummary(results)

	path, err := eval.SaveResults(results, resultsDir)
	if err != nil {
		log.Printf("Warning: could not save results: %v", err)
	} else {
		fmt.Printf("Results saved to %s\n", path)
	}
}

func cmdServe(dbPath string) {
	serveFlags := flag.NewFlagSet("serve", flag.ExitOnError)
	var addr string
	serveFlags.StringVar(&addr, "addr", ":8088", "Listen address")
	serveFlags.Parse(os.Args[2:])

	conn := openDB(dbPath)

	// Open conversations DB
	convDBPath := filepath.Join(filepath.Dir(dbPath), "conversations.db")
	convDB, err := db.OpenConversationsDB(convDBPath)
	if err != nil {
		log.Printf("Warning: could not open conversations DB: %v", err)
	}

	mux := http.NewServeMux()

	// API routes
	apiHandler := api.NewHandler(conn, convDB)
	mux.Handle("/api/", apiHandler)

	// Health check
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","commit":%q,"build_time":%q}`, commit, buildTime)
	})

	// Embedded frontend
	uiFS, _ := iofs.Sub(web.StaticFiles, "dist")
	fileServer := http.FileServer(http.FS(uiFS))
	mux.Handle("/", spaHandler{fs: fileServer, fsys: uiFS})

	log.Printf("slack-search %s listening on %s", commit, addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

// spaHandler serves static files, falling back to index.html for SPA routing.
type spaHandler struct {
	fs   http.Handler
	fsys iofs.FS
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	_, err := iofs.Stat(h.fsys, path)
	if err != nil {
		r.URL.Path = "/"
	}
	h.fs.ServeHTTP(w, r)
}

func printTable(r *search.Result) {
	if r == nil || len(r.Rows) == 0 {
		fmt.Println("(no results)")
		return
	}
	// Simple table output
	widths := make([]int, len(r.Columns))
	for i, c := range r.Columns {
		widths[i] = len(c)
	}
	strs := make([][]string, len(r.Rows))
	for i, row := range r.Rows {
		strs[i] = make([]string, len(row))
		for j, v := range row {
			s := fmt.Sprintf("%v", v)
			if v == nil {
				s = ""
			}
			strs[i][j] = s
			if len(s) > widths[j] {
				widths[j] = len(s)
			}
		}
	}
	// Cap column widths
	for i := range widths {
		if widths[i] > 80 {
			widths[i] = 80
		}
	}

	// Header
	for i, c := range r.Columns {
		fmt.Printf("%-*s  ", widths[i], c)
	}
	fmt.Println()
	for _, w := range widths {
		fmt.Printf("%s  ", strings.Repeat("─", w))
	}
	fmt.Println()
	// Rows
	for _, row := range strs {
		for j, s := range row {
			if len(s) > widths[j] {
				s = s[:widths[j]-1] + "…"
			}
			fmt.Printf("%-*s  ", widths[j], s)
		}
		fmt.Println()
	}
	fmt.Printf("\n%d row(s)\n", len(r.Rows))
}

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}
