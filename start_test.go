package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/server"
)

// swapStdio replaces os.Stdin and os.Stdout with pipes for the test and
// returns the ends the test talks to.
func swapStdio(t *testing.T) (stdin io.WriteCloser, stdout *bufio.Reader) {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
	})
	return inW, bufio.NewReader(outR)
}

func TestStartPrintsTheVersion(t *testing.T) {
	_, out := swapStdio(t)
	if code := start([]string{"--version"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	line, err := out.ReadString('\n')
	if err != nil || strings.TrimSpace(line) != server.VERSION {
		t.Fatalf("printed %q, %v", line, err)
	}
	if !strings.HasPrefix(flickr.UserAgent, "flickr-mcp-server/"+server.VERSION) {
		t.Fatalf("User-Agent = %q", flickr.UserAgent)
	}
}

// The real entry point: JSON-RPC over stdin and stdout, as an MCP client
// starts it.
func TestStartServesMCPOverStdio(t *testing.T) {
	isolate(t)
	in, out := swapStdio(t)
	done := make(chan int, 1)
	go func() { done <- start(nil) }()

	send := func(msg string) {
		if _, err := io.WriteString(in, msg+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	receive := func() map[string]any {
		line, err := out.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON-RPC: %q", line)
		}
		return m
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	init := receive()
	info := init["result"].(map[string]any)["serverInfo"].(map[string]any)
	if info["name"] != server.ServerName || info["version"] != server.VERSION {
		t.Fatalf("serverInfo = %v", info)
	}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	list := receive()
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools listed")
	}
	_ = in.Close()
	if code := <-done; code != 0 {
		t.Fatalf("closing stdin is a normal end, got exit %d", code)
	}
}

func TestRunSubcommandReadsTheRealStdin(t *testing.T) {
	dir := isolate(t)
	in, _ := swapStdio(t)
	if _, err := io.WriteString(in, "the-key\nthe-secret\n"); err != nil {
		t.Fatal(err)
	}
	if code := start([]string{"setup"}); code != 0 {
		t.Fatalf("setup: exit %d", code)
	}
	creds, err := flickr.LoadCredentials(dir)
	if err != nil || creds.APIKey != "the-key" || creds.APISecret != "the-secret" {
		t.Fatalf("saved %+v, %v", creds, err)
	}
	if code := start([]string{"auth", "--perms", "admin"}); code != 1 {
		t.Fatalf("auth with a bad permission: exit %d, want 1", code)
	}
}

func TestWithoutAHomeDirectory(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if code := start([]string{"whoami"}); code != 1 {
		t.Fatalf("subcommand: exit %d, want 1", code)
	}
	if _, err := buildServer(serverConfig{maxPerms: flickr.PermsRead}); err == nil {
		t.Fatal("buildServer needs the configuration directory")
	}
}

func TestLooseCredentialsStopTheServer(t *testing.T) {
	dir := isolate(t)
	if err := flickr.SaveCredentials(dir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir+"/credentials", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := buildServer(serverConfig{maxPerms: flickr.PermsRead}); err == nil {
		t.Fatal("a credentials file others can read must stop the server")
	}
}
