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
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.ngs.io/flickr-mcp-server/flickr"
	"go.ngs.io/flickr-mcp-server/server"
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
	flag.Parse()

	if *showVersion {
		_, _ = fmt.Fprintln(os.Stdout, server.VERSION)
		return
	}

	if err := server.Run(ctx, server.New()); err != nil && ctx.Err() == nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}

func subcommand(name string) (func(context.Context, *cli, []string) error, bool) {
	switch name {
	case "setup":
		return func(ctx context.Context, c *cli, args []string) error { return c.setup(ctx, args) }, true
	case "auth":
		return func(ctx context.Context, c *cli, args []string) error { return c.auth(ctx, args) }, true
	case "whoami":
		return func(ctx context.Context, c *cli, args []string) error { return c.whoami(ctx, args) }, true
	}
	return nil, false
}
