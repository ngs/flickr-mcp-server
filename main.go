// Command flickr-mcp-server exposes the Flickr API to MCP clients as tools.
//
// It is an MCP server on stdio. stdout carries the JSON-RPC stream, so every
// diagnostic goes to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"go.ngs.io/flickr-mcp-server/server"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("flickr-mcp-server: ")

	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		_, _ = fmt.Fprintln(os.Stdout, server.VERSION)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.Run(ctx, server.New()); err != nil && ctx.Err() == nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}
