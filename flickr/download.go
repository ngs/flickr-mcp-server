package flickr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Size is one entry of flickr.photos.getSizes.
type Size struct {
	Label  string `json:"label"`
	Width  any    `json:"width"`
	Height any    `json:"height"`
	Source string `json:"source"`
	Media  string `json:"media"`
}

// Sizes lists the sizes Flickr offers for a photo.
func (c *Client) Sizes(ctx context.Context, photoID string) ([]Size, error) {
	body, err := c.Call(ctx, Request{
		Method: "flickr.photos.getSizes",
		Params: url.Values{"photo_id": {photoID}},
		Sign:   c.Token != nil && c.Token.Token != "",
	})
	if err != nil {
		return nil, err
	}
	var res struct {
		Sizes struct {
			Size []Size `json:"size"`
		} `json:"sizes"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("reading getSizes: %w", err)
	}
	return res.Sizes.Size, nil
}

// PickSize returns the size with the given label (as flickr.photos.getSizes
// names it, e.g. "Original" or "Large 2048"); an unknown label lists the
// available ones.
func (c *Client) PickSize(ctx context.Context, photoID, label string) (Size, error) {
	sizes, err := c.Sizes(ctx, photoID)
	if err != nil {
		return Size{}, err
	}
	var labels []string
	for _, s := range sizes {
		if strings.EqualFold(s.Label, label) {
			return s, nil
		}
		labels = append(labels, s.Label)
	}
	return Size{}, fmt.Errorf("photo %s has no size %q; available: %s", photoID, label, strings.Join(labels, ", "))
}

// Fetch streams the file at source (a size's Source URL) to w.
func (c *Client) Fetch(ctx context.Context, source string, w io.Writer) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GET %s: HTTP %d", source, resp.StatusCode)
	}
	return io.Copy(w, resp.Body)
}

// Download writes the photo at the size with the given label to w.
func (c *Client) Download(ctx context.Context, photoID, label string, w io.Writer) (Size, int64, error) {
	size, err := c.PickSize(ctx, photoID, label)
	if err != nil {
		return Size{}, 0, err
	}
	n, err := c.Fetch(ctx, size.Source, w)
	return size, n, err
}
