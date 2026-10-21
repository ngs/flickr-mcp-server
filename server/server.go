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

const instructions = "Tools for the Flickr API. Each flickr_* tool calls the Flickr API method of the same name (dots replaced by underscores)."

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
	err := srv.Run(ctx, &mcp.StdioTransport{})
	switch {
	case err == nil, errors.Is(err, io.EOF), errors.Is(err, mcp.ErrConnectionClosed):
		return nil
	default:
		return fmt.Errorf("serving MCP over stdio: %w", err)
	}
}
