// Package tools registers the Flickr API methods as MCP tools.
package tools

//go:generate go run ../internal/gen -in ../api/methods.json -out zz_generated.go

import (
	"strings"

	"go.ngs.io/flickr-mcp-server/flickr"
)

// Method is one Flickr API method, as described in api/methods.json.
type Method struct {
	Name          string
	Description   string
	NeedsLogin    bool
	NeedsSigning  bool
	RequiredPerms int
	Post          bool
	Arguments     []Argument
}

// Argument is one method argument.
type Argument struct {
	Name        string
	Optional    bool
	Description string
}

// Perms returns the permission the method needs.
func (m Method) Perms() flickr.Perms { return flickr.Perms(m.RequiredPerms) }

// ToolName is the MCP tool name: the method name with dots as underscores.
func (m Method) ToolName() string { return strings.ReplaceAll(m.Name, ".", "_") }

// Namespace is the part after "flickr.", e.g. "photos" for
// flickr.photos.geo.getLocation.
func (m Method) Namespace() string {
	parts := strings.SplitN(m.Name, ".", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// Methods returns the generated method table.
func Methods() []Method { return methods }

// SnapshotSource reports where the method table came from ("docs" or
// "reflection").
func SnapshotSource() string { return snapshotSource }
