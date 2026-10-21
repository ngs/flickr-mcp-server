// Command flickr-mcp-server exposes the Flickr API to MCP clients as tools.
//
// Without a subcommand it is an MCP server on stdio. stdout carries the
// JSON-RPC stream, so every diagnostic goes to stderr.
//
// Subcommands:
//
//	setup   save the API key and secret (not echoed)
//	auth    authorize with Flickr and save the access token
//	whoami  check the saved token
//	snapshot  rebuild api/methods.json (developers)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/localfs"
	"go.ngs.io/flickr-mcp-server/server"
	"go.ngs.io/flickr-mcp-server/tools"
)

func main() { os.Exit(start(os.Args[1:])) }

// start does the process-wide setup (logging to stderr, the User-Agent,
// signals) and runs with the real stdin, stdout and stdio server.
func start(args []string) int {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("flickr-mcp-server: ")
	flickr.UserAgent = "flickr-mcp-server/" + server.VERSION + " (+https://github.com/ngs/flickr-mcp-server)"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, stop, args, os.Stdout, server.Run)
}

// run is main without the process-wide setup, so tests can drive it. stdout
// is only written by --version; serve runs the MCP server (server.Run).
func run(ctx context.Context, stop func(), args []string, stdout io.Writer, serve func(context.Context, *mcp.Server) error) int {
	if len(args) > 0 {
		if cmd, ok := subcommand(args[0]); ok {
			// After the first Ctrl-C has cancelled ctx, let a second one kill
			// the process the usual way.
			context.AfterFunc(ctx, stop)
			configDir, err := flickr.ConfigDir()
			if err != nil {
				log.Print(err)
				return 1
			}
			if err := cmd(ctx, newCLI(configDir, os.Stdin, os.Stderr), args[1:]); err != nil {
				log.Printf("%s: %v", args[0], err)
				return 1
			}
			return 0
		}
	}

	fs := flag.NewFlagSet("flickr-mcp-server", flag.ContinueOnError)
	showVersion := fs.Bool("version", false, "print the version and exit")
	maxPermsFlag := fs.String("max-perms", "read", "highest permission a tool may need: read, write or delete")
	namespacesFlag := fs.String("namespaces", "", "comma-separated namespaces to expose (e.g. photos,photosets); empty means all")
	uploadDirFlag := fs.String("upload-dir", "", "directory flickr_upload and flickr_replace may read from; empty leaves them out")
	downloadDirFlag := fs.String("download-dir", "", "directory flickr_download may write to; empty leaves it out")
	maxResponseFlag := fs.Int("max-response-bytes", tools.DefaultMaxResponseBytes, "longest response text returned to the client; longer ones are cut")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		_, _ = fmt.Fprintln(stdout, server.VERSION)
		return 0
	}

	maxPerms, err := flickr.ParsePerms(*maxPermsFlag)
	if err != nil || maxPerms == flickr.PermsNone {
		log.Print("--max-perms must be read, write or delete")
		return 2
	}
	srv, err := buildServer(serverConfig{
		maxPerms:         maxPerms,
		namespaces:       splitList(*namespacesFlag),
		maxResponseBytes: *maxResponseFlag,
		uploadDir:        *uploadDirFlag,
		downloadDir:      *downloadDirFlag,
	})
	if err != nil {
		log.Print(err)
		return 1
	}
	if err := serve(ctx, srv); err != nil && ctx.Err() == nil {
		log.Printf("%v", err)
		return 1
	}
	return 0
}

// buildServer loads the credentials and the token and registers the tools
// the permission ceiling allows. Missing credentials are not fatal: the
// server starts so the client can show why calls fail.
type serverConfig struct {
	maxPerms         flickr.Perms
	namespaces       []string
	maxResponseBytes int
	uploadDir        string
	downloadDir      string
}

func buildServer(cfg serverConfig) (*mcp.Server, error) {
	maxPerms := cfg.maxPerms
	configDir, err := flickr.ConfigDir()
	if err != nil {
		return nil, err
	}
	creds, err := flickr.LoadCredentials(configDir)
	if err != nil {
		return nil, err
	}
	if !creds.Complete() {
		log.Printf("no API key and secret; run `flickr-mcp-server setup`")
	}
	var token *flickr.Token
	switch t, err := flickr.LoadToken(configDir); {
	case err == nil:
		token = &t
		if t.Perms < maxPerms {
			log.Printf("the saved token only has %s permission; limiting tools to %s (run `flickr-mcp-server auth --perms %s` for more)", t.Perms, t.Perms, maxPerms)
			maxPerms = t.Perms
		}
	case errors.Is(err, os.ErrNotExist):
		log.Printf("not authorized; only methods that need no login are available (run `flickr-mcp-server auth`)")
	default:
		return nil, err
	}
	srv := server.New()
	client := flickr.NewClient(creds, token)
	n := tools.Register(srv, client, tools.Options{
		MaxPerms:         maxPerms,
		Authorized:       token != nil,
		Namespaces:       cfg.namespaces,
		MaxResponseBytes: cfg.maxResponseBytes,
	})
	fileOpts := tools.FileOptions{MaxPerms: maxPerms, Authorized: token != nil}
	if cfg.uploadDir != "" {
		dir, err := localfs.Open(cfg.uploadDir)
		if err != nil {
			return nil, fmt.Errorf("--upload-dir: %w", err)
		}
		fileOpts.UploadDir = &dir
	}
	if cfg.downloadDir != "" {
		dir, err := localfs.Open(cfg.downloadDir)
		if err != nil {
			return nil, fmt.Errorf("--download-dir: %w", err)
		}
		fileOpts.DownloadDir = &dir
	}
	n += tools.RegisterFiles(srv, client, fileOpts)
	log.Printf("registered %d tools (max permission %s, method list from %s)", n, maxPerms, tools.SnapshotSource())
	return srv, nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func subcommand(name string) (func(context.Context, *cli, []string) error, bool) {
	switch name {
	case "setup":
		return func(ctx context.Context, c *cli, args []string) error { return c.setup(ctx, args) }, true
	case "auth":
		return func(ctx context.Context, c *cli, args []string) error { return c.auth(ctx, args) }, true
	case "whoami":
		return func(ctx context.Context, c *cli, args []string) error { return c.whoami(ctx, args) }, true
	case "snapshot":
		return func(ctx context.Context, c *cli, args []string) error { return c.snapshotCmd(ctx, args) }, true
	}
	return nil, false
}
