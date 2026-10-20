package mcpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/martinpovolny/slack-search/internal/db"
)

func TestServeHTTPProtocol(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()

	serverReady := make(chan *http.Server, 1)
	originalStart := serveHTTPStart
	serveHTTPStart = func(streamable *server.StreamableHTTPServer, _ string) error {
		mux := http.NewServeMux()
		mux.Handle("/mcp", streamable)
		httpServer := &http.Server{Handler: mux}
		serverReady <- httpServer
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	t.Cleanup(func() {
		serveHTTPStart = originalStart
		listener.Close()
	})

	serveDone := make(chan error, 1)
	go func() { serveDone <- ServeHTTP(conn, nil, addr) }()
	httpServer := <-serverReady
	baseURL := "http://" + addr

	mcpClient, err := client.NewStreamableHttpClient(baseURL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mcpClient.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := mcpClient.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
	}}); err != nil {
		t.Fatal(err)
	}
	result, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 8 {
		t.Fatalf("tools = %d, want 8", len(result.Tools))
	}
	response, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("ServeHTTP = %v", err)
	}
}

func TestStreamableHTTP(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	streamable := server.NewStreamableHTTPServer(newServer(conn, nil))
	mux := http.NewServeMux()
	mux.Handle("/mcp", streamable)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	mcpClient, err := client.NewStreamableHttpClient(httpServer.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mcpClient.Close() })
	ctx := context.Background()
	if err := mcpClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = mcpClient.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "1.0.0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(result.Tools))
	for i, tool := range result.Tools {
		names[i] = tool.Name
	}
	sort.Strings(names)
	want := []string{"slack_bookmarks", "slack_canvases", "slack_channels", "slack_grep", "slack_refresh_channel", "slack_schema", "slack_sql", "slack_thread"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
	call, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "slack_schema"}})
	if err != nil {
		t.Fatal(err)
	}
	if call.IsError || len(call.Content) == 0 {
		t.Fatalf("schema result = %#v, want successful content", call)
	}
	response, err := http.Get(httpServer.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestServeHTTPReturnsListenerError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := ServeHTTP(nil, nil, listener.Addr().String()); err == nil {
		t.Fatal("ServeHTTP returned nil, want address-in-use error")
	}
}
