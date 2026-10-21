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

// Download writes the photo at the size with the given label (as
// flickr.photos.getSizes names it, e.g. "Original" or "Large 2048") to w.
func (c *Client) Download(ctx context.Context, photoID, label string, w io.Writer) (Size, int64, error) {
	sizes, err := c.Sizes(ctx, photoID)
	if err != nil {
		return Size{}, 0, err
	}
	var chosen *Size
	var labels []string
	for i := range sizes {
		labels = append(labels, sizes[i].Label)
		if strings.EqualFold(sizes[i].Label, label) {
			chosen = &sizes[i]
		}
	}
	if chosen == nil {
		return Size{}, 0, fmt.Errorf("photo %s has no size %q; available: %s", photoID, label, strings.Join(labels, ", "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chosen.Source, nil)
	if err != nil {
		return Size{}, 0, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Size{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Size{}, 0, fmt.Errorf("GET %s: HTTP %d", chosen.Source, resp.StatusCode)
	}
	n, err := io.Copy(w, resp.Body)
	return *chosen, n, err
}
