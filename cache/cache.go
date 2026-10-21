// Package cache keeps downloaded images in the download directory under
// predictable names, so a photo already on disk is not fetched again.
//
// A cached file is named <photo id>_<secret>_<size>.<ext>. The secret comes
// from the image URL Flickr returns; it changes when the photo is replaced
// (and for the original size it is the separate original secret), so a stale
// copy is never served under a new name. Nothing is removed automatically:
// Scan reports the usage and the Plan functions choose what to remove when
// asked.
package cache

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	namePattern = regexp.MustCompile(`^[0-9]+_[0-9a-f]+_[a-z0-9-]+\.[a-z0-9]+$`)
	idPattern   = regexp.MustCompile(`^[0-9]+$`)
	hexPattern  = regexp.MustCompile(`^[0-9a-f]+$`)
	nonSlug     = regexp.MustCompile(`[^a-z0-9]+`)
)

// Name builds the cache file name for a photo at a size, from the size's
// source URL (as flickr.photos.getSizes lists it).
func Name(photoID, source, label string) (string, error) {
	if !idPattern.MatchString(photoID) {
		return "", fmt.Errorf("%q is not a photo id", photoID)
	}
	u, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("reading the image URL: %w", err)
	}
	base := path.Base(u.Path)
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(base)), ".")
	parts := strings.Split(strings.TrimSuffix(base, path.Ext(base)), "_")
	if ext == "" || len(parts) < 2 || parts[0] != photoID || !hexPattern.MatchString(parts[1]) {
		return "", fmt.Errorf("%s is not an image file of photo %s", source, photoID)
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	return fmt.Sprintf("%s_%s_%s.%s", photoID, parts[1], slug, ext), nil
}

// Matches reports whether name follows the cache naming convention. Only
// such files are counted and only such files can be purged.
func Matches(name string) bool { return namePattern.MatchString(name) }

// Entry is one cached file.
type Entry struct {
	Name     string    `json:"name"`
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	Modified time.Time `json:"modified"`
}

// Stats is the cache usage, entries oldest first.
type Stats struct {
	Bytes   int64   `json:"bytes"`
	Files   int     `json:"files"`
	Entries []Entry `json:"-"`
}

// Scan reads the cache files directly in dir.
func Scan(dir string) (Stats, error) {
	var s Stats
	items, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	for _, item := range items {
		if !item.Type().IsRegular() || !Matches(item.Name()) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue // removed while we looked
		}
		s.Entries = append(s.Entries, Entry{Name: item.Name(), Path: filepath.Join(dir, item.Name()), Bytes: info.Size(), Modified: info.ModTime()})
		s.Bytes += info.Size()
	}
	s.Files = len(s.Entries)
	sort.SliceStable(s.Entries, func(i, j int) bool { return s.Entries[i].Modified.Before(s.Entries[j].Modified) })
	return s, nil
}

// PlanToSize returns the oldest entries to remove so that the cache is at
// most target bytes.
func PlanToSize(s Stats, target int64) []Entry {
	var out []Entry
	total := s.Bytes
	for _, e := range s.Entries {
		if total <= target {
			break
		}
		out = append(out, e)
		total -= e.Bytes
	}
	return out
}

// PlanOlderThan returns the entries last used before cutoff.
func PlanOlderThan(s Stats, cutoff time.Time) []Entry {
	var out []Entry
	for _, e := range s.Entries {
		if e.Modified.Before(cutoff) {
			out = append(out, e)
		}
	}
	return out
}

// Remove deletes the entries and returns the bytes freed. It stops at the
// first failure.
func Remove(entries []Entry) (int64, error) {
	var freed int64
	for _, e := range entries {
		if err := os.Remove(e.Path); err != nil {
			return freed, err
		}
		freed += e.Bytes
	}
	return freed, nil
}

// Touch marks a cached file as just used, so purges by age keep it.
func Touch(p string) error {
	now := time.Now()
	return os.Chtimes(p, now, now)
}

// Human formats a byte count with binary units.
func Human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
