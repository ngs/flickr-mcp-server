package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/snapshot"
)

// cli carries what the subcommands read and write, so tests can drive them.
// Everything a person reads goes to errOut (stderr): stdout is reserved for
// the MCP stream even when no server is running.
type cli struct {
	in        *bufio.Reader
	errOut    io.Writer
	configDir string
	// readSecret reads a value without echoing it. nil reads a plain line
	// from in (pipes and tests).
	readSecret func() (string, error)
	// saveTerminal records the terminal state before a secret prompt and
	// returns the function that restores it if the prompt is cancelled
	// (term.ReadPassword restores it only when it returns). nil for pipes.
	saveTerminal func() (restore func())
	newClient    func(flickr.Credentials, *flickr.Token) *flickr.Client
	openURL      func(string) error
}

func newCLI(configDir string) *cli {
	c := &cli{
		in:        bufio.NewReader(os.Stdin),
		errOut:    os.Stderr,
		configDir: configDir,
		newClient: flickr.NewClient,
		openURL:   openBrowser,
	}
	fd := int(os.Stdin.Fd()) // #nosec G115 -- a file descriptor fits in int
	if term.IsTerminal(fd) {
		c.readSecret = func() (string, error) {
			b, err := term.ReadPassword(fd)
			_, _ = fmt.Fprintln(c.errOut)
			return string(b), err
		}
		c.saveTerminal = func() func() {
			state, err := term.GetState(fd)
			if err != nil {
				return func() {}
			}
			return func() { _ = term.Restore(fd, state) }
		}
	}
	return c
}

func (c *cli) printf(format string, args ...any) { _, _ = fmt.Fprintf(c.errOut, format, args...) }

// readLine reads one line, or gives up when ctx is cancelled (Ctrl-C).
func (c *cli) readLine(ctx context.Context) (string, error) {
	return untilCancelled(ctx, func() (string, error) {
		line, err := c.in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || line == "") {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}, nil)
}

// secret reads a value without echoing it when stdin is a terminal.
func (c *cli) secret(ctx context.Context) (string, error) {
	if c.readSecret == nil {
		return c.readLine(ctx)
	}
	restore := func() {}
	if c.saveTerminal != nil {
		restore = c.saveTerminal()
	}
	v, err := untilCancelled(ctx, c.readSecret, restore)
	return strings.TrimSpace(v), err
}

// untilCancelled runs read in the background and returns its result, or
// ctx.Err() as soon as ctx is cancelled (after calling onCancel). The reader
// is abandoned then; the command is about to exit.
func untilCancelled(ctx context.Context, read func() (string, error), onCancel func()) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		v   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := read()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		if onCancel != nil {
			onCancel()
		}
		return "", ctx.Err()
	}
}

// setup asks for the API key and secret (neither is echoed) and saves them.
func (c *cli) setup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.printf("Create a non-commercial key at https://www.flickr.com/services/apps/create/\n")
	c.printf("API key: ")
	key, err := c.secret(ctx)
	if err != nil {
		return err
	}
	c.printf("API secret: ")
	secret, err := c.secret(ctx)
	if err != nil {
		return err
	}
	if key == "" || secret == "" {
		return errors.New("both the key and the secret are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := flickr.SaveCredentials(c.configDir, flickr.Credentials{APIKey: key, APISecret: secret}); err != nil {
		return err
	}
	c.printf("Saved the key and the secret to %s/credentials (mode 0600).\nNext: flickr-mcp-server auth --perms read\n", c.configDir)
	return nil
}

// auth runs the OAuth 1.0a flow with an out-of-band verifier and saves the
// access token.
func (c *cli) auth(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	permsFlag := fs.String("perms", "read", "permission to request: read, write or delete")
	open := fs.Bool("open", false, "open the authorization page in a browser")
	start := fs.Bool("start", false, "only print the authorization URL and keep the request token for --finish")
	finish := fs.String("finish", "", "exchange this code (from the authorization page) for the access token")
	if err := fs.Parse(args); err != nil {
		return err
	}
	perms, err := flickr.ParsePerms(*permsFlag)
	if err != nil || perms == flickr.PermsNone {
		return fmt.Errorf("--perms must be read, write or delete")
	}
	creds, err := c.credentials()
	if err != nil {
		return err
	}
	client := c.newClient(creds, nil)
	if *finish != "" {
		pending, err := flickr.LoadPending(c.configDir)
		if err != nil {
			return fmt.Errorf("no authorization in progress (run auth --start first): %w", err)
		}
		token, err := client.AccessToken(ctx, pending.Token, pending.TokenSecret, *finish, pending.Perms)
		if err != nil {
			return err
		}
		if err := flickr.SaveToken(c.configDir, token); err != nil {
			return err
		}
		_ = flickr.RemovePending(c.configDir)
		c.printf("Authorized as %s with %s permission. Saved to %s/token.json (mode 0600).\n", token.Username, token.Perms, c.configDir)
		return nil
	}
	reqToken, reqSecret, authURL, err := client.RequestToken(ctx, perms)
	if err != nil {
		return err
	}
	if *start {
		if err := flickr.SavePending(c.configDir, flickr.Pending{Token: reqToken, TokenSecret: reqSecret, Perms: perms}); err != nil {
			return err
		}
		c.printf("Open this page, allow access, then run: flickr-mcp-server auth --finish <code>\n\n  %s\n", authURL)
		return nil
	}
	c.printf("Open this page, allow access, and paste the code Flickr shows:\n\n  %s\n\n", authURL)
	if *open {
		if err := c.openURL(authURL); err != nil {
			c.printf("(could not open a browser: %v)\n", err)
		}
	}
	c.printf("Code: ")
	verifier, err := c.readLine(ctx)
	if err != nil {
		return err
	}
	if verifier == "" {
		return errors.New("no code entered")
	}
	token, err := client.AccessToken(ctx, reqToken, reqSecret, verifier, perms)
	if err != nil {
		return err
	}
	if err := flickr.SaveToken(c.configDir, token); err != nil {
		return err
	}
	c.printf("Authorized as %s (%s) with %s permission. Saved to %s/token.json (mode 0600).\n", token.Username, token.UserNSID, token.Perms, c.configDir)
	return nil
}

// whoami checks the saved token with flickr.test.login.
func (c *cli) whoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	creds, err := c.credentials()
	if err != nil {
		return err
	}
	token, err := flickr.LoadToken(c.configDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return flickr.ErrNotAuthorized
		}
		return err
	}
	body, err := c.newClient(creds, &token).Call(ctx, flickr.Request{Method: "flickr.test.login", Sign: true})
	if err != nil {
		return err
	}
	var res struct {
		User struct {
			ID       string `json:"id"`
			Username struct {
				Content string `json:"_content"`
			} `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return err
	}
	c.printf("%s (%s), token permission: %s\n", res.User.Username.Content, res.User.ID, token.Perms)
	return nil
}

func (c *cli) credentials() (flickr.Credentials, error) {
	creds, err := flickr.LoadCredentials(c.configDir)
	if err != nil {
		return creds, err
	}
	if !creds.Complete() {
		return creds, fmt.Errorf("no API key and secret; run `flickr-mcp-server setup` or set %s and %s", flickr.EnvAPIKey, flickr.EnvAPISecret)
	}
	return creds, nil
}

func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u) // #nosec G204 -- the URL is the Flickr authorize URL we built
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u) // #nosec G204
	default:
		cmd = exec.Command("xdg-open", u) // #nosec G204
	}
	return cmd.Start()
}

// snapshotCmd writes api/methods.json from the documentation pages (no key
// needed) or from the reflection API (needs the API key).
func (c *cli) snapshotCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	source := fs.String("source", "docs", "docs or reflection")
	out := fs.String("o", "api/methods.json", "output file")
	delay := fs.Duration("delay", 500*time.Millisecond, "pause between requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var s *snapshot.Snapshot
	var err error
	switch *source {
	case "docs":
		s, err = snapshot.FromDocs(ctx, snapshot.HTTPFetcher(&http.Client{Timeout: 30 * time.Second}), snapshot.DocsBaseURL, *delay)
	case "reflection":
		creds, cerr := flickr.LoadCredentials(c.configDir)
		if cerr != nil {
			return cerr
		}
		if creds.APIKey == "" {
			return fmt.Errorf("the reflection source needs an API key; run `flickr-mcp-server setup` or set %s", flickr.EnvAPIKey)
		}
		s, err = snapshot.FromReflection(ctx, c.newClient(creds, nil), *delay)
	default:
		return fmt.Errorf("unknown source %q (want docs or reflection)", *source)
	}
	if err != nil {
		return err
	}
	if err := s.Write(*out); err != nil {
		return err
	}
	c.printf("Wrote %d methods from %s to %s\n", len(s.Methods), s.Source, *out)
	return nil
}
