package tools

import "go.ngs.io/flickr-mcp-server/flickr"

var _ FileClient = (*flickr.Client)(nil)
