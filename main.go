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

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("flickr-mcp-server: ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 {
		if run, ok := subcommand(os.Args[1]); ok {
			// After the first Ctrl-C has cancelled ctx, let a second one kill
			// the process the usual way.
			context.AfterFunc(ctx, stop)
			configDir, err := flickr.ConfigDir()
			if err != nil {
				log.Fatal(err)
			}
			if err := run(ctx, newCLI(configDir), os.Args[2:]); err != nil {
				log.Printf("%s: %v", os.Args[1], err)
				os.Exit(1)
			}
			return
		}
	}

	showVersion := flag.Bool("version", false, "print the version and exit")
	maxPermsFlag := flag.String("max-perms", "read", "highest permission a tool may need: read, write or delete")
	namespacesFlag := flag.String("namespaces", "", "comma-separated namespaces to expose (e.g. photos,photosets); empty means all")
	uploadDirFlag := flag.String("upload-dir", "", "directory flickr_upload and flickr_replace may read from; empty leaves them out")
	downloadDirFlag := flag.String("download-dir", "", "directory flickr_download may write to; empty leaves it out")
	maxResponseFlag := flag.Int("max-response-bytes", tools.DefaultMaxResponseBytes, "longest response text returned to the client; longer ones are cut")
	flag.Parse()

	if *showVersion {
		_, _ = fmt.Fprintln(os.Stdout, server.VERSION)
		return
	}

	maxPerms, err := flickr.ParsePerms(*maxPermsFlag)
	if err != nil || maxPerms == flickr.PermsNone {
		log.Fatalf("--max-perms must be read, write or delete")
	}
	srv, err := buildServer(serverConfig{
		maxPerms:         maxPerms,
		namespaces:       splitList(*namespacesFlag),
		maxResponseBytes: *maxResponseFlag,
		uploadDir:        *uploadDirFlag,
		downloadDir:      *downloadDirFlag,
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Run(ctx, srv); err != nil && ctx.Err() == nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
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
