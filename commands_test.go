package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.ngs.io/flickr-mcp-server/flickr"
)

func testCLI(t *testing.T, input string, srv *httptest.Server) (*cli, *bytes.Buffer) {
	t.Helper()
	t.Setenv(flickr.EnvAPIKey, "")
	t.Setenv(flickr.EnvAPISecret, "")
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
	if err := c.setup(nil); err != nil {
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
	if err := c.setup(nil); err == nil {
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
