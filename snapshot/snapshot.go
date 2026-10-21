// Package snapshot builds api/methods.json, the description of every Flickr
// API method the tools are generated from.
//
// Two sources produce the same format:
//   - the API documentation pages (https://www.flickr.com/services/api/),
//     which need no API key;
//   - flickr.reflection.getMethods and flickr.reflection.getMethodInfo, which
//     need an API key and are the authoritative source.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Snapshot is the content of api/methods.json.
type Snapshot struct {
	// Source is "docs" or "reflection".
	Source  string   `json:"source"`
	Methods []Method `json:"methods"`
}

// Method describes one API method.
type Method struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// NeedsLogin is true when the method only works for an authorized user.
	NeedsLogin bool `json:"needslogin"`
	// NeedsSigning is true when the call must be OAuth-signed.
	NeedsSigning bool `json:"needssigning"`
	// RequiredPerms is 0 (none), 1 (read), 2 (write) or 3 (delete).
	RequiredPerms int `json:"requiredperms"`
	// Post is true when the method must be called with HTTP POST.
	Post      bool       `json:"post"`
	Arguments []Argument `json:"arguments"`
}

// Argument describes one method argument. api_key is never listed: the
// server adds it.
type Argument struct {
	Name        string `json:"name"`
	Optional    bool   `json:"optional"`
	Description string `json:"description"`
}

// Normalize sorts methods by name and drops api_key, so snapshots from either
// source diff cleanly.
func (s *Snapshot) Normalize() {
	sort.Slice(s.Methods, func(i, j int) bool { return s.Methods[i].Name < s.Methods[j].Name })
	for i := range s.Methods {
		m := &s.Methods[i]
		args := m.Arguments[:0]
		for _, a := range m.Arguments {
			if a.Name == "api_key" {
				continue
			}
			args = append(args, a)
		}
		m.Arguments = args
		if m.RequiredPerms > 0 {
			m.NeedsLogin = true
			m.NeedsSigning = true
		}
	}
}

// Write saves the snapshot as indented JSON.
func (s *Snapshot) Write(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Read loads a snapshot.
func Read(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path given by the developer
	if err != nil {
		return nil, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &s, nil
}

// cleanText collapses whitespace in text extracted from HTML.
func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
