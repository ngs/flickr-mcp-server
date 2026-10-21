package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"

	"go.ngs.io/flickr-mcp-server/flickr"
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
	newClient  func(flickr.Credentials, *flickr.Token) *flickr.Client
	openURL    func(string) error
}

func newCLI(configDir string) *cli {
	c := &cli{
		in:        bufio.NewReader(os.Stdin),
		errOut:    os.Stderr,
		configDir: configDir,
		newClient: flickr.NewClient,
		openURL:   openBrowser,
	}
	if term.IsTerminal(int(os.Stdin.Fd())) { // #nosec G115 -- a file descriptor fits in int
		c.readSecret = func() (string, error) {
			b, err := term.ReadPassword(int(os.Stdin.Fd())) // #nosec G115
			_, _ = fmt.Fprintln(c.errOut)
			return string(b), err
		}
	}
	return c
}

func (c *cli) printf(format string, args ...any) { _, _ = fmt.Fprintf(c.errOut, format, args...) }

func (c *cli) readLine() (string, error) {
	line, err := c.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (c *cli) secret() (string, error) {
	if c.readSecret != nil {
		v, err := c.readSecret()
		return strings.TrimSpace(v), err
	}
	return c.readLine()
}

// setup asks for the API key and secret (neither is echoed) and saves them.
func (c *cli) setup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(c.errOut)
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.printf("Create a non-commercial key at https://www.flickr.com/services/apps/create/\n")
	c.printf("API key: ")
	key, err := c.secret()
	if err != nil {
		return err
	}
	c.printf("API secret: ")
	secret, err := c.secret()
	if err != nil {
		return err
	}
	if key == "" || secret == "" {
		return errors.New("both the key and the secret are required")
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
	reqToken, reqSecret, authURL, err := client.RequestToken(ctx, perms)
	if err != nil {
		return err
	}
	c.printf("Open this page, allow access, and paste the code Flickr shows:\n\n  %s\n\n", authURL)
	if *open {
		if err := c.openURL(authURL); err != nil {
			c.printf("(could not open a browser: %v)\n", err)
		}
	}
	c.printf("Code: ")
	verifier, err := c.readLine()
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
