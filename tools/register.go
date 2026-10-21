package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.ngs.io/flickr-mcp-server/flickr"
)

// Caller performs Flickr REST calls (flickr.Client in production).
type Caller interface {
	Call(ctx context.Context, r flickr.Request) (json.RawMessage, error)
}

// Options control which tools are registered and how they call Flickr.
type Options struct {
	// MaxPerms is the highest permission a registered tool may need.
	MaxPerms flickr.Perms
	// Authorized is true when a user token is available. Calls are then
	// always signed, so they see what the user can see; without a token only
	// methods that need no permission are registered.
	Authorized bool
	// Namespaces limits the tools to these namespaces (e.g. "photos").
	// Empty means all.
	Namespaces []string
	// MaxResponseBytes caps the text returned to the client; longer responses
	// are cut and say so. 0 means DefaultMaxResponseBytes.
	MaxResponseBytes int
}

// DefaultMaxResponseBytes keeps one call from flooding the client's context.
const DefaultMaxResponseBytes = 256 * 1024

// DefaultPerPage is sent when a paged method is called without per_page.
// Flickr's own default is 100 and its maximum 500.
const DefaultPerPage = "50"

// excluded are the methods of the retired pre-OAuth authentication API and
// the OAuth token helpers; authorization is done with the auth command.
func excluded(m Method) bool { return strings.HasPrefix(m.Name, "flickr.auth.") }

// Selected returns the methods Register would register, in name order.
func Selected(opts Options) []Method {
	ceiling := opts.MaxPerms
	if !opts.Authorized {
		ceiling = flickr.PermsNone
	}
	wanted := map[string]bool{}
	for _, ns := range opts.Namespaces {
		wanted[strings.TrimSpace(ns)] = true
	}
	var out []Method
	for _, m := range methods {
		if excluded(m) || m.Perms() > ceiling {
			continue
		}
		if !opts.Authorized && (m.NeedsLogin || m.NeedsSigning) {
			continue
		}
		if len(wanted) > 0 && !wanted[m.Namespace()] {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Register adds one tool per selected method to srv.
func Register(srv *mcp.Server, c Caller, opts Options) int {
	selected := Selected(opts)
	limit := opts.MaxResponseBytes
	if limit <= 0 {
		limit = DefaultMaxResponseBytes
	}
	for _, m := range selected {
		srv.AddTool(toolFor(m), handlerFor(m, c, opts.Authorized, limit))
	}
	return len(selected)
}

func toolFor(m Method) *mcp.Tool {
	properties := map[string]any{}
	required := []string{}
	for _, a := range m.Arguments {
		properties[a.Name] = map[string]any{
			// Flickr takes every argument as a string; numbers and booleans
			// are accepted and sent in their usual text form.
			"type":        []string{"string", "number", "integer", "boolean"},
			"description": a.Description,
		}
		if !a.Optional {
			required = append(required, a.Name)
		}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}

	var notes []string
	notes = append(notes, "Flickr API method "+m.Name+".")
	if hasArgument(m, "per_page") {
		notes = append(notes, "Paged: per_page defaults to "+DefaultPerPage+" here (Flickr allows up to 500); pass page to get the next page, and use pages and total in the response to know when to stop.")
	}
	if hasArgument(m, "extras") {
		notes = append(notes, "Ask only for the extras you need: each one adds to every item in the response.")
	}
	if m.Perms() > flickr.PermsNone {
		notes = append(notes, fmt.Sprintf("Needs %s permission.", m.Perms()))
	}
	return &mcp.Tool{
		Name:        m.ToolName(),
		Description: strings.TrimSpace(m.Description + "\n\n" + strings.Join(notes, " ")),
		InputSchema: schema,
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    !m.Post && m.Perms() <= flickr.PermsRead,
			DestructiveHint: boolPtr(m.Perms() == flickr.PermsDelete),
			OpenWorldHint:   boolPtr(true),
		},
	}
}

func handlerFor(m Method, c Caller, authorized bool, limit int) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := buildParams(m, req.Params.Arguments)
		if err != nil {
			return errorResult(err), nil
		}
		if hasArgument(m, "per_page") && params.Get("per_page") == "" {
			params.Set("per_page", DefaultPerPage)
		}
		body, err := c.Call(ctx, flickr.Request{
			Method: m.Name,
			Params: params,
			Sign:   authorized,
			Post:   m.Post,
		})
		if err != nil {
			return errorResult(err), nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: truncate(string(body), limit)}}}, nil
	}
}

// truncate cuts text to limit bytes (on a UTF-8 boundary) and says how much
// was dropped and how to get less.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n\n[truncated: showing %d of %d bytes. The JSON above is incomplete. Ask for less (a smaller per_page, fewer extras) or start the server with a larger --max-response-bytes.]", cut, len(text))
}

func hasArgument(m Method, name string) bool {
	for _, a := range m.Arguments {
		if a.Name == name {
			return true
		}
	}
	return false
}

// buildParams turns the tool arguments into Flickr parameters, rejecting
// unknown names and missing required ones before anything is sent.
func buildParams(m Method, raw json.RawMessage) (url.Values, error) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	known := map[string]Argument{}
	for _, a := range m.Arguments {
		known[a.Name] = a
	}
	params := url.Values{}
	for name, v := range args {
		if _, ok := known[name]; !ok {
			return nil, fmt.Errorf("unknown argument %q for %s", name, m.Name)
		}
		s, err := scalar(v)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", name, err)
		}
		params.Set(name, s)
	}
	var missing []string
	for _, a := range m.Arguments {
		if !a.Optional && params.Get(a.Name) == "" {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required argument(s) for %s: %s", m.Name, strings.Join(missing, ", "))
	}
	return params, nil
}

func scalar(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case nil:
		return "", nil
	default:
		return "", errors.New("must be a string, a number or a boolean")
	}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

func boolPtr(b bool) *bool { return &b }
