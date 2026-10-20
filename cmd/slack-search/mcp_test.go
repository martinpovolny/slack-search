package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestParseMCPOptions(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      mcpOptions
		wantError bool
	}{
		{name: "defaults", want: mcpOptions{transport: "stdio", addr: "127.0.0.1:8000"}},
		{name: "explicit stdio", args: []string{"--transport", "stdio"}, want: mcpOptions{transport: "stdio", addr: "127.0.0.1:8000"}},
		{name: "streamable http", args: []string{"--transport", "streamable-http"}, want: mcpOptions{transport: "streamable-http", addr: "127.0.0.1:8000"}},
		{name: "custom address", args: []string{"--transport", "streamable-http", "--addr", "0.0.0.0:8000"}, want: mcpOptions{transport: "streamable-http", addr: "0.0.0.0:8000"}},
		{name: "invalid transport", args: []string{"--transport", "sse"}, wantError: true},
		{name: "unknown option", args: []string{"--bogus"}, wantError: true},
		{name: "unexpected positional argument", args: []string{"unexpected"}, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMCPOptions(tc.args)
			if (err != nil) != tc.wantError {
				t.Fatalf("parseMCPOptions() error = %v, wantError %v", err, tc.wantError)
			}
			if err == nil && got != tc.want {
				t.Fatalf("parseMCPOptions() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDispatchMCP(t *testing.T) {
	t.Run("stdio", func(t *testing.T) {
		stdioCalls, httpCalls := 0, 0
		err := dispatchMCP(mcpOptions{transport: "stdio", addr: "127.0.0.1:8000"}, func() error {
			stdioCalls++
			return nil
		}, func(string) error {
			httpCalls++
			return nil
		})
		if err != nil {
			t.Fatalf("dispatchMCP() error = %v", err)
		}
		if stdioCalls != 1 || httpCalls != 0 {
			t.Fatalf("callbacks called stdio=%d http=%d, want stdio=1 http=0", stdioCalls, httpCalls)
		}
	})

	t.Run("streamable http", func(t *testing.T) {
		stdioCalls, httpCalls := 0, 0
		const addr = "0.0.0.0:9000"
		err := dispatchMCP(mcpOptions{transport: "streamable-http", addr: addr}, func() error {
			stdioCalls++
			return nil
		}, func(gotAddr string) error {
			httpCalls++
			if gotAddr != addr {
				t.Errorf("HTTP address = %q, want %q", gotAddr, addr)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("dispatchMCP() error = %v", err)
		}
		if stdioCalls != 0 || httpCalls != 1 {
			t.Fatalf("callbacks called stdio=%d http=%d, want stdio=0 http=1", stdioCalls, httpCalls)
		}
	})

	t.Run("propagates HTTP error", func(t *testing.T) {
		wantErr := errors.New("listener failed")
		gotErr := dispatchMCP(mcpOptions{transport: "streamable-http"}, func() error {
			t.Fatal("stdio callback invoked")
			return nil
		}, func(string) error { return wantErr })
		if gotErr != wantErr {
			t.Fatalf("dispatchMCP() error = %v, want %v", gotErr, wantErr)
		}
	})
}

func TestMCPOptionsDoNotCreateDataDirectoryBeforeParsing(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "slack-search")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}

	for _, tc := range []struct {
		name          string
		args          []string
		wantExitError bool
	}{
		{name: "help", args: []string{"mcp", "--help"}},
		{name: "invalid option", args: []string{"mcp", "--bogus"}, wantExitError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := exec.Command(binary, tc.args...)
			cmd.Env = append(os.Environ(), "HOME="+home)
			output, err := cmd.CombinedOutput()
			if tc.wantExitError && err == nil {
				t.Fatalf("command succeeded, want failure; output:\n%s", output)
			}
			if !tc.wantExitError && err != nil {
				t.Fatalf("command failed: %v\n%s", err, output)
			}
			if _, err := os.Stat(filepath.Join(home, ".slack-search")); !os.IsNotExist(err) {
				t.Fatalf("MCP command created data directory: stat error = %v", err)
			}
		})
	}
}
