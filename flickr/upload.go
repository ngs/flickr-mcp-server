package flickr

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// Default upload endpoints. They are not REST methods and return XML.
const (
	DefaultUploadURL  = "https://up.flickr.com/services/upload/"
	DefaultReplaceURL = "https://up.flickr.com/services/replace/"
)

// UploadResult is what the upload and replace endpoints return: a photo id,
// or a ticket id for an asynchronous upload (see
// flickr.photos.upload.checkTickets).
type UploadResult struct {
	PhotoID  string `json:"photo_id,omitempty"`
	TicketID string `json:"ticket_id,omitempty"`
}

// Upload sends a new photo or video. fields are the optional upload
// arguments (title, description, tags, is_public, ..., async).
func (c *Client) Upload(ctx context.Context, path string, fields url.Values) (UploadResult, error) {
	return c.sendFile(ctx, c.uploadURL(), path, fields)
}

// Replace replaces the file of an existing photo.
func (c *Client) Replace(ctx context.Context, path, photoID string, fields url.Values) (UploadResult, error) {
	f := url.Values{}
	for k, v := range fields {
		f[k] = v
	}
	f.Set("photo_id", photoID)
	return c.sendFile(ctx, c.replaceURL(), path, f)
}

func (c *Client) uploadURL() string {
	if c.UploadURL != "" {
		return c.UploadURL
	}
	return DefaultUploadURL
}

func (c *Client) replaceURL() string {
	if c.ReplaceURL != "" {
		return c.ReplaceURL
	}
	return DefaultReplaceURL
}

// sendFile posts a multipart form: the signed fields, then the file as
// "photo". The file is not part of the signature (Flickr's rule).
func (c *Client) sendFile(ctx context.Context, endpoint, path string, fields url.Values) (UploadResult, error) {
	if c.Token == nil || c.Token.Token == "" {
		return UploadResult{}, ErrNotAuthorized
	}
	params := url.Values{}
	for k, vs := range fields {
		params[k] = append([]string(nil), vs...)
	}
	params.Set("api_key", c.Creds.APIKey)
	c.signer.sign(http.MethodPost, endpoint, params, c.Creds.APIKey, c.Creds.APISecret, c.Token.Token, c.Token.TokenSecret)

	file, err := os.Open(path) // #nosec G304 -- the caller confines the path
	if err != nil {
		return UploadResult{}, err
	}
	defer func() { _ = file.Close() }()

	// The multipart writer writes to a bytes.Buffer, which never fails, so
	// only reading the file can.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, vs := range params {
		for _, v := range vs {
			_ = w.WriteField(k, v)
		}
	}
	part, _ := w.CreateFormFile("photo", filepath.Base(path))
	if _, err := io.Copy(part, file); err != nil {
		return UploadResult{}, err
	}
	_ = w.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return UploadResult{}, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return UploadResult{}, fmt.Errorf("POST %s: %w", normalizeURL(endpoint), err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return UploadResult{}, err
	}
	if resp.StatusCode/100 != 2 {
		return UploadResult{}, fmt.Errorf("POST %s: HTTP %d", normalizeURL(endpoint), resp.StatusCode)
	}
	return parseUploadResponse(data)
}

func parseUploadResponse(data []byte) (UploadResult, error) {
	var rsp struct {
		Stat     string `xml:"stat,attr"`
		PhotoID  string `xml:"photoid"`
		TicketID string `xml:"ticketid"`
		Err      struct {
			Code int    `xml:"code,attr"`
			Msg  string `xml:"msg,attr"`
		} `xml:"err"`
	}
	if err := xml.Unmarshal(data, &rsp); err != nil {
		return UploadResult{}, fmt.Errorf("reading the upload response: %w", err)
	}
	if rsp.Stat != "ok" {
		return UploadResult{}, &APIError{Code: rsp.Err.Code, Message: rsp.Err.Msg}
	}
	return UploadResult{PhotoID: rsp.PhotoID, TicketID: rsp.TicketID}, nil
}
