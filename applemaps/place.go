package applemaps

import (
	"context"
	"errors"
	"net/url"
)

const (
	placePath        = "/v1/place"
	alternateIDsPath = "/v1/place/alternateIds"
)

// Place looks up a single place by its Apple place ID.
//
// The lookup returns no more data than a search result does: Apple's Place object
// carries no opening hours, rating, price level, or photo at any endpoint, so
// this is a way to refresh or resolve an ID rather than to enrich a place.
func (c *Client) Place(ctx context.Context, id, lang string) (*Place, error) {
	if id == "" {
		return nil, errors.New("applemaps: Place requires an id")
	}

	params := url.Values{}
	c.applyLang(params, lang)

	// Apple place IDs are opaque, so they can contain characters that would
	// otherwise change the path's structure. PathEscape keeps an id with a slash
	// or a question mark from being read as extra path segments or a query.
	var resp Place
	if err := c.get(ctx, placePath+"/"+url.PathEscape(id), params, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Places looks up several places in one call.
//
// The response can partially succeed: PlacesResponse carries both Results and
// Errors, and a populated Errors does not mean Results is empty. Both are
// returned so a caller can act on the good records and still see which IDs
// failed — dropping either half silently loses information.
func (c *Client) Places(ctx context.Context, ids []string, lang string) (*PlacesResponse, error) {
	if len(ids) == 0 {
		return nil, errors.New("applemaps: Places requires at least one id")
	}

	params := url.Values{}
	setStrings(params, "ids", ids)
	c.applyLang(params, lang)

	var resp PlacesResponse
	if err := c.get(ctx, placePath, params, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// AlternateIDs returns the alternate place IDs for one or more place IDs.
//
// Apple place IDs are not stable forever; an ID that stops resolving may have an
// alternate that still does. Like Places, this can partially succeed.
func (c *Client) AlternateIDs(ctx context.Context, ids []string) (*AlternateIDsResponse, error) {
	if len(ids) == 0 {
		return nil, errors.New("applemaps: AlternateIDs requires at least one id")
	}

	params := url.Values{}
	setStrings(params, "ids", ids)

	var resp AlternateIDsResponse
	if err := c.get(ctx, alternateIDsPath, params, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
