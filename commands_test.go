package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.ngs.io/flickr-mcp-server/flickr"
)

func testCLI(t *testing.T, input string, srv *httptest.Server) (*cli, *bytes.Buffer) {
	t.Helper()
	t.Setenv(flickr.EnvAPIKey, "")
	t.Setenv(flickr.EnvAPISecret, "")
	t.Setenv(flickr.EnvCredentialsFile, "")
	var out bytes.Buffer
	c := &cli{
		in:        bufio.NewReader(strings.NewReader(input)),
		errOut:    &out,
		configDir: t.TempDir(),
		openURL:   func(string) error { return nil },
		newClient: func(creds flickr.Credentials, token *flickr.Token) *flickr.Client {
			client := flickr.NewClient(creds, token)
			if srv != nil {
				client.RESTURL = srv.URL + "/services/rest/"
				client.RequestTokenURL = srv.URL + "/oauth/request_token"
				client.AccessTokenURL = srv.URL + "/oauth/access_token"
			}
			return client
		},
	}
	return c, &out
}

func TestSetupSavesWithoutEchoingTheSecret(t *testing.T) {
	c, out := testCLI(t, "", nil)
	answers := []string{"the-key", "the-secret"}
	c.readSecret = func() (string, error) {
		v := answers[0]
		answers = answers[1:]
		return v, nil
	}
	if err := c.setup(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "the-secret") || strings.Contains(out.String(), "the-key") {
		t.Fatalf("output shows a credential: %q", out.String())
	}
	creds, err := flickr.LoadCredentials(c.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if creds.APIKey != "the-key" || creds.APISecret != "the-secret" {
		t.Fatalf("saved %+v", creds)
	}
}

func TestSetupRequiresBothValues(t *testing.T) {
	c, _ := testCLI(t, "key\n\n", nil)
	if err := c.setup(context.Background(), nil); err == nil {
		t.Fatal("want an error for an empty secret")
	}
}

func flickrStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/request_token":
			_, _ = io.WriteString(w, "oauth_token=req&oauth_token_secret=reqsecret")
		case "/oauth/access_token":
			_, _ = io.WriteString(w, "oauth_token=acc&oauth_token_secret=accsecret&user_nsid=12%40N01&username=jane")
		case "/services/rest/":
			if r.URL.Query().Get("oauth_token") != "acc" {
				_, _ = io.WriteString(w, `{"stat":"fail","code":98,"message":"Invalid auth token"}`)
				return
			}
			_, _ = io.WriteString(w, `{"stat":"ok","user":{"id":"12@N01","username":{"_content":"jane"}}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAuthThenWhoami(t *testing.T) {
	srv := flickrStub(t)
	c, out := testCLI(t, "123-456-789\n", srv)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(context.Background(), []string{"--perms", "write"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "oauth_token=req&perms=write") {
		t.Fatalf("authorize URL missing: %q", out.String())
	}
	token, err := flickr.LoadToken(c.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "acc" || token.Perms != flickr.PermsWrite || token.Username != "jane" {
		t.Fatalf("token = %+v", token)
	}
	if strings.Contains(out.String(), "accsecret") {
		t.Fatal("the token secret must not be printed")
	}

	out.Reset()
	if err := c.whoami(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "jane (12@N01), token permission: write") {
		t.Fatalf("whoami output = %q", out.String())
	}
}

func TestAuthWithoutCredentialsFails(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	if err := c.auth(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "setup") {
		t.Fatalf("err = %v", err)
	}
}

func TestWhoamiWithoutTokenSaysToAuthorize(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.whoami(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthRejectsUnknownPerms(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	if err := c.auth(context.Background(), []string{"--perms", "admin"}); err == nil {
		t.Fatal("want an error")
	}
}

// Ctrl-C while a prompt waits for input ends the command, and setup saves
// nothing.
func TestSetupStopsOnCancellation(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	c.readSecret = func() (string, error) { <-block; return "late", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.setup(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(c.configDir, "credentials")); !os.IsNotExist(err) {
		t.Fatal("setup must not save after cancellation")
	}
}

func TestAuthStopsOnCancellationAtTheCodePrompt(t *testing.T) {
	srv := flickrStub(t)
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	c, _ := testCLI(t, "", srv)
	c.in = bufio.NewReader(pr)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.auth(ctx, nil) }()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(c.configDir, "token.json")); !os.IsNotExist(err) {
		t.Fatal("auth must not save a token after cancellation")
	}
}

// auth --start only prints the URL and keeps the request token; auth
// --finish exchanges a code later, from another process.
func TestTwoStepAuth(t *testing.T) {
	srv := flickrStub(t)
	c, out := testCLI(t, "", srv)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(context.Background(), []string{"--perms", "delete", "--start"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "oauth_token=req&perms=delete") || strings.Contains(out.String(), "reqsecret") {
		t.Fatalf("start output = %q", out.String())
	}
	pendingPath := filepath.Join(c.configDir, "pending")
	if info, err := os.Stat(pendingPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(c.configDir, "token.json")); !os.IsNotExist(err) {
		t.Fatal("start must not save a token")
	}

	out.Reset()
	if err := c.auth(context.Background(), []string{"--finish", "123-456"}); err != nil {
		t.Fatal(err)
	}
	token, err := flickr.LoadToken(c.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "acc" || token.Perms != flickr.PermsDelete {
		t.Fatalf("token = %+v (the permission comes from --start)", token)
	}
	if _, err := os.Stat(pendingPath); !os.IsNotExist(err) {
		t.Fatal("finish must remove the pending request token")
	}
	if strings.Contains(out.String(), "accsecret") {
		t.Fatal("the token secret must not be printed")
	}
}

func TestFinishWithoutStartFails(t *testing.T) {
	c, _ := testCLI(t, "", flickrStub(t))
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(context.Background(), []string{"--finish", "1"}); err == nil || !strings.Contains(err.Error(), "--start") {
		t.Fatalf("err = %v", err)
	}
}

func TestSnapshotFromDocs(t *testing.T) {
	c, out := testCLI(t, "", nil)
	pages := map[string]string{
		"https://docs.test/":                      `<a href="/services/api/flickr.test.echo.html">echo</a>`,
		"https://docs.test/flickr.test.echo.html": `<div class="InfoCase"><h1>flickr.test.echo</h1><div class="MethodDescription">Echo.</div><h3>Authentication</h3><p>This method does not require authentication.</p><h3>Arguments</h3><dl><dt><b><code>api_key</code></b> (Required)</dt><dd>Key.</dd></dl>`,
	}
	c.docsBaseURL = "https://docs.test/"
	c.docs = func(_ context.Context, url string) (string, error) {
		p, ok := pages[url]
		if !ok {
			return "", errors.New("unexpected " + url)
		}
		return p, nil
	}
	path := filepath.Join(t.TempDir(), "methods.json")
	if err := c.snapshotCmd(context.Background(), []string{"--source", "docs", "-o", path, "--delay", "0"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"flickr.test.echo"`) || !strings.Contains(string(b), `"source": "docs"`) {
		t.Fatalf("snapshot = %s, %v", b, err)
	}
	if !strings.Contains(out.String(), "Wrote 1 methods from docs") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSnapshotFromReflection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("method") {
		case "flickr.reflection.getMethods":
			_, _ = io.WriteString(w, `{"stat":"ok","methods":{"method":[{"_content":"flickr.test.null"}]}}`)
		case "flickr.reflection.getMethodInfo":
			_, _ = io.WriteString(w, `{"stat":"ok","method":{"name":"flickr.test.null","needslogin":1,"needssigning":1,"requiredperms":1,"description":{"_content":"Null."}},"arguments":{"argument":[]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	c, _ := testCLI(t, "", srv)
	path := filepath.Join(t.TempDir(), "methods.json")
	if err := c.snapshotCmd(context.Background(), []string{"--source", "reflection", "-o", path}); err == nil {
		t.Fatal("reflection needs an API key")
	}
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.snapshotCmd(context.Background(), []string{"--source", "reflection", "-o", path, "--delay", "0"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"requiredperms": 1`) {
		t.Fatalf("snapshot = %s", b)
	}
}

func TestSnapshotRejectsBadUse(t *testing.T) {
	c, _ := testCLI(t, "", nil)
	if err := c.snapshotCmd(context.Background(), []string{"--source", "elsewhere"}); err == nil {
		t.Fatal("unknown source")
	}
	if err := c.snapshotCmd(context.Background(), []string{"--bogus"}); err == nil {
		t.Fatal("unknown flag")
	}
	c.docs = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	if err := c.snapshotCmd(context.Background(), []string{"-o", filepath.Join(t.TempDir(), "m.json")}); err == nil {
		t.Fatal("a fetch failure must be reported")
	}
}

func TestBrowserCommand(t *testing.T) {
	cases := map[string]string{"darwin": "open", "windows": "rundll32", "linux": "xdg-open", "freebsd": "xdg-open"}
	for goos, want := range cases {
		name, args := browserCommand(goos, "https://x/")
		if name != want || args[len(args)-1] != "https://x/" {
			t.Errorf("%s: %s %v", goos, name, args)
		}
	}
}

func TestNewCLIOutsideATerminal(t *testing.T) {
	c := newCLI(t.TempDir())
	if c.docs == nil || c.docsBaseURL == "" || c.newClient == nil || c.openURL == nil {
		t.Fatal("newCLI must wire its defaults")
	}
}

func TestAuthOpensTheBrowserWhenAsked(t *testing.T) {
	srv := flickrStub(t)
	c, out := testCLI(t, "1-2-3\n", srv)
	var opened string
	c.openURL = func(u string) error { opened = u; return errors.New("no browser here") }
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(context.Background(), []string{"--open"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(opened, "oauth_token=req") || !strings.Contains(out.String(), "could not open a browser") {
		t.Fatalf("opened %q, output %q", opened, out.String())
	}
}

func TestAuthNeedsACode(t *testing.T) {
	c, _ := testCLI(t, "\n", flickrStub(t))
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.auth(context.Background(), nil); err == nil {
		t.Fatal("an empty code must fail")
	}
}

func TestWhoamiReportsAPIErrors(t *testing.T) {
	srv := flickrStub(t)
	c, _ := testCLI(t, "", srv)
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: "k", APISecret: "s"}); err != nil {
		t.Fatal(err)
	}
	// The stub rejects any token other than "acc".
	if err := flickr.SaveToken(c.configDir, flickr.Token{Token: "stale", TokenSecret: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := c.whoami(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "Invalid auth token") {
		t.Fatalf("err = %v", err)
	}
}
