package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/creack/pty"

	"go.ngs.io/flickr-mcp-server/flickr"
)

// On a terminal, secrets are read without echo and the terminal state is
// restored when a prompt is abandoned.
func TestNewCLIOnATerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no pseudo-terminals on Windows")
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	var out strings.Builder
	c := newCLI(t.TempDir(), tty, &out)
	if c.readSecret == nil || c.saveTerminal == nil {
		t.Fatal("a terminal must get the no-echo prompt")
	}
	restore := c.saveTerminal()
	if _, err := io.WriteString(ptmx, "s3cret\r"); err != nil {
		t.Fatal(err)
	}
	got, err := c.readSecret()
	if err != nil || got != "s3cret" {
		t.Fatalf("read %q, %v", got, err)
	}
	restore()
	_ = tty.Close()
	// With the terminal gone, saving its state fails and there is nothing to restore.
	c.saveTerminal()()
}

func TestCancelledSecretPromptRestoresTheTerminal(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	started := make(chan struct{})
	c.readSecret = func() (string, error) { close(started); <-block; return "", nil }
	restored := false
	c.saveTerminal = func() func() { return func() { restored = true } }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.secret(ctx); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) || !restored {
		t.Fatalf("err = %v, restored = %v", err, restored)
	}
}

func TestSetupFailures(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	if err := c.setup(context.Background(), []string{"--bogus"}); err == nil {
		t.Error("an unknown flag must fail")
	}

	c, _ = testCLI(t, "", nil)
	if err := c.setup(context.Background(), nil); err == nil {
		t.Error("no input must fail")
	}

	// Ctrl-C while the secret is typed: nothing is saved.
	c, _ = testCLI(t, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	answers := 0
	c.readSecret = func() (string, error) {
		answers++
		if answers == 2 {
			cancel()
		}
		return "v", nil
	}
	if err := c.setup(ctx, nil); err == nil {
		t.Error("cancellation during the prompts must fail")
	}

	c, _ = testCLI(t, "k\ns\n", nil)
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	c.configDir = file
	if err := c.setup(context.Background(), nil); err == nil {
		t.Error("an unwritable configuration directory must fail")
	}
}

// failingStub answers every OAuth and REST call with an HTTP error.
func failingStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// blockPath makes path a non-empty directory, so writing a file there fails.
func blockPath(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func authCLI(t *testing.T, input string, srv *httptest.Server) *cli {
	t.Helper()
	c, _ := testCLI(t, input, srv)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAuthFailures(t *testing.T) {
	ctx := context.Background()
	c := authCLI(t, "", flickrStub(t))
	if err := c.auth(ctx, []string{"--bogus"}); err == nil {
		t.Error("an unknown flag must fail")
	}
	if err := c.auth(ctx, nil); err == nil {
		t.Error("no code at all (end of input) must fail")
	}

	c = authCLI(t, "1\n", failingStub(t))
	if err := c.auth(ctx, nil); err == nil {
		t.Error("a failing request token must fail")
	}

	// The request token works, the exchange does not.
	ok := flickrStub(t)
	half := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/access_token" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		ok.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(half.Close)
	c = authCLI(t, "1\n", half)
	if err := c.auth(ctx, nil); err == nil {
		t.Error("a failing exchange must fail")
	}
	c = authCLI(t, "", half)
	if err := flickr.SavePending(c.configDir, flickr.Pending{Token: "r", TokenSecret: "rs", Perms: flickr.PermsRead}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(ctx, []string{"--finish", "1"}); err == nil {
		t.Error("a failing exchange in --finish must fail")
	}

	// The exchange works but the token cannot be saved.
	c = authCLI(t, "1\n", flickrStub(t))
	blockPath(t, filepath.Join(c.configDir, "token.json"))
	if err := c.auth(ctx, nil); err == nil {
		t.Error("an unsavable token must fail")
	}
	c = authCLI(t, "", flickrStub(t))
	_ = flickr.SavePending(c.configDir, flickr.Pending{Token: "r", TokenSecret: "rs", Perms: flickr.PermsRead})
	blockPath(t, filepath.Join(c.configDir, "token.json"))
	if err := c.auth(ctx, []string{"--finish", "1"}); err == nil {
		t.Error("an unsavable token in --finish must fail")
	}

	c = authCLI(t, "", flickrStub(t))
	blockPath(t, filepath.Join(c.configDir, "pending"))
	if err := c.auth(ctx, []string{"--start"}); err == nil {
		t.Error("an unsavable pending token must fail")
	}
}

func TestWhoamiFailures(t *testing.T) {
	ctx := context.Background()
	c := authCLI(t, "", flickrStub(t))
	if err := c.whoami(ctx, []string{"--bogus"}); err == nil {
		t.Error("an unknown flag must fail")
	}
	if err := flickr.SaveToken(c.configDir, flickr.Token{Token: "acc", TokenSecret: "x"}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(filepath.Join(c.configDir, "token.json"), 0o644)
		if err := c.whoami(ctx, nil); !errors.Is(err, flickr.ErrLoosePermissions) {
			t.Errorf("a loose token file: %v", err)
		}
		_ = os.Chmod(filepath.Join(c.configDir, "token.json"), 0o600)
	}

	weird := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"stat":"ok","user":"nobody"}`)
	}))
	t.Cleanup(weird.Close)
	c = authCLI(t, "", weird)
	_ = flickr.SaveToken(c.configDir, flickr.Token{Token: "t", TokenSecret: "x"})
	if err := c.whoami(ctx, nil); err == nil {
		t.Error("an unexpected reply must fail")
	}
}

func TestLooseCredentialsAreRefusedByEveryCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mode bits on Windows")
	}
	c := authCLI(t, "", nil)
	_ = os.Chmod(filepath.Join(c.configDir, "credentials"), 0o644)
	for name, err := range map[string]error{
		"auth":     c.auth(context.Background(), nil),
		"snapshot": c.snapshotCmd(context.Background(), []string{"--source", "reflection"}),
	} {
		if !errors.Is(err, flickr.ErrLoosePermissions) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSnapshotCannotWrite(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	c.docsBaseURL = "https://docs.test/"
	c.docs = func(_ context.Context, url string) (string, error) {
		if url == "https://docs.test/" {
			return `<a href="/services/api/flickr.test.echo.html">e</a>`, nil
		}
		return `<div class="InfoCase"><h1>flickr.test.echo</h1><h3>Authentication</h3><p>This method does not require authentication.</p><h3>`, nil
	}
	if err := c.snapshotCmd(context.Background(), []string{"-o", filepath.Join(t.TempDir(), "no", "dir", "m.json")}); err == nil {
		t.Fatal("an unwritable output must fail")
	}
}
