// Package server builds the MCP server and serves it over stdio.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerName is the implementation name reported in the initialize handshake.
const ServerName = "flickr-mcp-server"

const instructions = "Tools for the Flickr API. Each flickr_* tool calls the Flickr API method of the same name with dots replaced by underscores (flickr_photos_search is flickr.photos.search) and returns Flickr's JSON. " +
	"Use \"me\" as user_id for the authorized account. Paged methods return 50 items unless per_page says otherwise; follow page and pages to continue. " +
	"Only tools within the server's permission ceiling are listed. flickr_upload, flickr_replace and flickr_download, when present, work only with paths relative to the directories the server was started with."

// New builds the MCP server. Tools are registered by the caller.
func New() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Title:   "Flickr",
		Version: VERSION,
	}, &mcp.ServerOptions{Instructions: instructions})
}

// Run serves srv over stdio until the client goes away or ctx is cancelled.
//
// The client closing stdin is how a normal shutdown looks from here, so an EOF
// is reported as a clean return rather than a failure.
func Run(ctx context.Context, srv *mcp.Server) error {
	return RunTransport(ctx, srv, &mcp.StdioTransport{})
}

// RunTransport is Run over any transport.
func RunTransport(ctx context.Context, srv *mcp.Server, t mcp.Transport) error {
	err := srv.Run(ctx, t)
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, mcp.ErrConnectionClosed):
		return nil
	default:
		return fmt.Errorf("serving MCP over stdio: %w", err)
	}
}
