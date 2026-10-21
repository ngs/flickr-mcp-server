package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/server"
	"go.ngs.io/flickr-mcp-server/tools"
)

// isolate points the configuration directory at a fresh temporary one and
// hides the developer's own FLICKR_* variables.
func isolate(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv(flickr.EnvAPIKey, "")
	t.Setenv(flickr.EnvAPISecret, "")
	t.Setenv(flickr.EnvCredentialsFile, "")
	return filepath.Join(base, "flickr-mcp-server")
}

func authorize(t *testing.T, dir string, perms flickr.Perms) {
	t.Helper()
	if err := flickr.SaveCredentials(dir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := flickr.SaveToken(dir, flickr.Token{Token: "t", TokenSecret: "ts", Perms: perms}); err != nil {
		t.Fatal(err)
	}
}

// countTools is a serve function that lists the tools instead of serving
// stdio.
func countTools(n *int) func(context.Context, *mcp.Server) error {
	return func(ctx context.Context, srv *mcp.Server) error {
		st, ct := mcp.NewInMemoryTransports()
		if _, err := srv.Connect(ctx, st, nil); err != nil {
			return err
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			return err
		}
		defer func() { _ = cs.Close() }()
		for _, err := range cs.Tools(ctx, nil) {
			if err != nil {
				return err
			}
			*n++
		}
		return nil
	}
}

func noServe(context.Context, *mcp.Server) error { return errors.New("must not serve") }

func TestRunPrintsTheVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), func() {}, []string{"--version"}, &out, noServe); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if strings.TrimSpace(out.String()) != server.VERSION {
		t.Fatalf("printed %q", out.String())
	}
}

func TestRunRejectsBadFlags(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"--max-perms", "admin"}, {"--max-perms", "none"}, {"--no-such-flag"}} {
		if code := run(context.Background(), func() {}, args, &bytes.Buffer{}, noServe); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestRunRegistersToolsWithinTheTokensPermission(t *testing.T) {
	dir := isolate(t)
	authorize(t, dir, flickr.PermsRead)
	var n int
	// Asking for delete with a read token gives the read set.
	if code := run(context.Background(), func() {}, []string{"--max-perms", "delete"}, &bytes.Buffer{}, countTools(&n)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := len(tools.Selected(tools.Options{MaxPerms: flickr.PermsRead, Authorized: true})); n != want {
		t.Fatalf("%d tools, want %d", n, want)
	}
}

func TestRunWithFileDirectoriesAndNamespaces(t *testing.T) {
	dir := isolate(t)
	authorize(t, dir, flickr.PermsWrite)
	files := t.TempDir()
	var n int
	args := []string{"--max-perms", "write", "--namespaces", "photosets", "--upload-dir", files, "--download-dir", files}
	if code := run(context.Background(), func() {}, args, &bytes.Buffer{}, countTools(&n)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := len(tools.Selected(tools.Options{MaxPerms: flickr.PermsWrite, Authorized: true, Namespaces: []string{"photosets"}})) + 5
	if n != want {
		t.Fatalf("%d tools, want %d", n, want)
	}
}

func TestRunWithoutCredentialsStillStarts(t *testing.T) {
	isolate(t)
	var n int
	if code := run(context.Background(), func() {}, nil, &bytes.Buffer{}, countTools(&n)); code != 0 || n == 0 {
		t.Fatalf("exit %d with %d tools", code, n)
	}
}

func TestRunFailsOnBadDirectoriesAndLooseFiles(t *testing.T) {
	dir := isolate(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{{"--upload-dir", missing}, {"--download-dir", missing}} {
		if code := run(context.Background(), func() {}, args, &bytes.Buffer{}, noServe); code != 1 {
			t.Errorf("%v: exit %d, want 1", args, code)
		}
	}
	authorize(t, dir, flickr.PermsRead)
	if err := os.Chmod(filepath.Join(dir, "token.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run(context.Background(), func() {}, nil, &bytes.Buffer{}, noServe); code != 1 {
		t.Fatalf("a loose token file: exit %d, want 1", code)
	}
}

func TestRunReportsServeErrors(t *testing.T) {
	isolate(t)
	if code := run(context.Background(), func() {}, nil, &bytes.Buffer{}, noServe); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := run(ctx, func() {}, nil, &bytes.Buffer{}, noServe); code != 0 {
		t.Fatalf("an error after cancellation is a normal exit, got %d", code)
	}
}

func TestRunDispatchesSubcommands(t *testing.T) {
	isolate(t)
	if code := run(context.Background(), func() {}, []string{"whoami"}, &bytes.Buffer{}, noServe); code != 1 {
		t.Fatalf("whoami without credentials: exit %d, want 1", code)
	}
	if code := run(context.Background(), func() {}, []string{"snapshot", "--source", "nowhere"}, &bytes.Buffer{}, noServe); code != 1 {
		t.Fatalf("snapshot with a bad source: exit %d, want 1", code)
	}
	for _, name := range []string{"setup", "auth", "whoami", "snapshot"} {
		if _, ok := subcommand(name); !ok {
			t.Errorf("%s is not a subcommand", name)
		}
	}
	if _, ok := subcommand("serve"); ok {
		t.Error("serve is not a subcommand")
	}
}

func TestSplitList(t *testing.T) {
	got := splitList(" photos, ,tags ,")
	if strings.Join(got, "|") != "photos|tags" {
		t.Fatalf("got %q", got)
	}
	if splitList("") != nil {
		t.Fatal("an empty list is nil")
	}
}
