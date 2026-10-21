package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"go.ngs.io/flickr-mcp-server/flickr"
)

// Caller is the part of flickr.Client the reflection source needs.
type Caller interface {
	Call(ctx context.Context, r flickr.Request) (json.RawMessage, error)
}

// flexInt reads Flickr's numbers, which come as numbers or strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		*f = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	*f = flexInt(n)
	return err
}

type content struct {
	Content string `json:"_content"`
}

// FromReflection builds a snapshot with flickr.reflection.getMethods and
// flickr.reflection.getMethodInfo. It needs only an API key. delay spaces
// out the calls (Flickr allows 3600 calls per hour per key).
func FromReflection(ctx context.Context, c Caller, delay time.Duration) (*Snapshot, error) {
	body, err := c.Call(ctx, flickr.Request{Method: "flickr.reflection.getMethods"})
	if err != nil {
		return nil, err
	}
	var list struct {
		Methods struct {
			Method []content `json:"method"`
		} `json:"methods"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("reading getMethods: %w", err)
	}
	s := &Snapshot{Source: "reflection"}
	for i, entry := range list.Methods.Method {
		if i > 0 && delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		m, err := methodInfo(ctx, c, entry.Content)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Content, err)
		}
		s.Methods = append(s.Methods, m)
	}
	s.Normalize()
	return s, nil
}

func methodInfo(ctx context.Context, c Caller, name string) (Method, error) {
	body, err := c.Call(ctx, flickr.Request{Method: "flickr.reflection.getMethodInfo", Params: url.Values{"method_name": {name}}})
	if err != nil {
		return Method{}, err
	}
	var info struct {
		Method struct {
			Name          string  `json:"name"`
			NeedsLogin    flexInt `json:"needslogin"`
			NeedsSigning  flexInt `json:"needssigning"`
			RequiredPerms flexInt `json:"requiredperms"`
			Description   content `json:"description"`
		} `json:"method"`
		Arguments struct {
			Argument []struct {
				Name     string  `json:"name"`
				Optional flexInt `json:"optional"`
				Content  string  `json:"_content"`
			} `json:"argument"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return Method{}, fmt.Errorf("reading getMethodInfo: %w", err)
	}
	m := Method{
		Name:          info.Method.Name,
		Description:   htmlText(info.Method.Description.Content),
		NeedsLogin:    info.Method.NeedsLogin == 1,
		NeedsSigning:  info.Method.NeedsSigning == 1,
		RequiredPerms: int(info.Method.RequiredPerms),
		// Reflection does not say which methods need POST; Flickr requires it
		// for every method that changes data.
		Post: info.Method.RequiredPerms >= 2,
	}
	for _, a := range info.Arguments.Argument {
		m.Arguments = append(m.Arguments, Argument{Name: a.Name, Optional: a.Optional == 1, Description: htmlText(a.Content)})
	}
	return m, nil
}
