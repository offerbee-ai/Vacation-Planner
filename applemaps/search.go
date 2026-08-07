package applemaps

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

const (
	searchPath             = "/v1/search"
	searchAutocompletePath = "/v1/searchAutocomplete"

	// DefaultMaxSearchPages bounds SearchAll when a caller passes no explicit
	// limit. Each page is a billable call against the daily quota, so an
	// unbounded walk of a broad query could spend a large share of it on one
	// request.
	DefaultMaxSearchPages = 5
)

// SearchRequest describes a /v1/search call.
//
// Apple requires Q: there is no way to search purely by category or by area.
// SearchLocation and SearchRegion only bias results and do not constrain them,
// and there is no radius parameter and no result limit. A caller that needs
// results within a fixed distance must filter them after the fact.
type SearchRequest struct {
	// Q is the place to search for. Required.
	Q string
	// IncludePoiCategories restricts results to these categories. Apple's
	// taxonomy is coarse — all general retail is PoiCategoryStore — so this
	// narrows far less than it appears to. Carry fine distinctions in Q.
	IncludePoiCategories []PoiCategory
	// ExcludePoiCategories removes these categories from results.
	ExcludePoiCategories []PoiCategory
	// LimitToCountries is a list of two-letter ISO 3166-1 codes.
	LimitToCountries []string
	// ResultTypeFilter restricts which kinds of result come back.
	ResultTypeFilter []SearchResultType
	// IncludeAddressCategories requires SearchResultTypeAddress in
	// ResultTypeFilter; Apple rejects it otherwise.
	IncludeAddressCategories []AddressCategory
	// ExcludeAddressCategories carries the same requirement.
	ExcludeAddressCategories []AddressCategory
	// Lang overrides the client's default language for this request.
	Lang string
	// SearchLocation biases results toward a coordinate.
	SearchLocation *Location
	// SearchRegion biases results toward a bounding box.
	SearchRegion *MapRegion
	// UserLocation is used as a fallback bias when SearchLocation is unset.
	UserLocation *Location
	// SearchRegionPriority indicates how strongly to weight SearchRegion.
	SearchRegionPriority string
	// EnablePagination asks Apple to return paginated results. SearchAll sets
	// this itself.
	EnablePagination bool
	// PageToken requests a specific page. SearchAll manages this itself.
	PageToken string
}

func (r SearchRequest) params(c *Client) url.Values {
	params := url.Values{}
	params.Set("q", r.Q)
	setCategories(params, "includePoiCategories", r.IncludePoiCategories)
	setCategories(params, "excludePoiCategories", r.ExcludePoiCategories)
	setStrings(params, "limitToCountries", r.LimitToCountries)

	if len(r.ResultTypeFilter) > 0 {
		values := make([]string, len(r.ResultTypeFilter))
		for i, t := range r.ResultTypeFilter {
			values[i] = string(t)
		}
		setStrings(params, "resultTypeFilter", values)
	}
	setAddressCategories(params, "includeAddressCategories", r.IncludeAddressCategories)
	setAddressCategories(params, "excludeAddressCategories", r.ExcludeAddressCategories)

	c.applyLang(params, r.Lang)
	if r.SearchLocation != nil {
		params.Set("searchLocation", formatLocation(r.SearchLocation.Latitude, r.SearchLocation.Longitude))
	}
	if r.SearchRegion != nil {
		params.Set("searchRegion", formatRegion(*r.SearchRegion))
	}
	if r.UserLocation != nil {
		params.Set("userLocation", formatLocation(r.UserLocation.Latitude, r.UserLocation.Longitude))
	}
	if r.SearchRegionPriority != "" {
		params.Set("searchRegionPriority", r.SearchRegionPriority)
	}
	if r.EnablePagination {
		params.Set("enablePagination", "true")
	}
	if r.PageToken != "" {
		params.Set("pageToken", r.PageToken)
	}
	return params
}

func setAddressCategories(params url.Values, key string, categories []AddressCategory) {
	if len(categories) == 0 {
		return
	}
	values := make([]string, len(categories))
	for i, c := range categories {
		values[i] = string(c)
	}
	setStrings(params, key, values)
}

// Search returns one page of results.
//
// Unlike Geocode, an empty result set is not an error: a search for places of a
// kind that genuinely do not exist nearby is a valid, informative answer, and
// callers scanning an area need to distinguish it from a failure.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	if req.Q == "" {
		return nil, errors.New("applemaps: Search requires Q")
	}

	var resp SearchResponse
	if err := c.get(ctx, searchPath, req.params(c), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SearchAllResult is the outcome of a paginated search.
type SearchAllResult struct {
	// Places is every result across the pages fetched.
	Places []SearchPlace
	// Pages is how many pages were fetched.
	Pages int
	// Truncated reports that the page limit was reached while Apple was still
	// offering a next page, so Places is incomplete. Callers that care about
	// completeness must check this — silently returning a short list would read
	// as "that is all there is".
	Truncated bool
	// DisplayMapRegion is the region from the first page.
	DisplayMapRegion *SearchMapRegion
}

// SearchAll walks a paginated search and accumulates every result.
//
// maxPages bounds the walk; zero or negative means DefaultMaxSearchPages. This
// function owns pagination and nothing else — it applies no distance filter and
// no ranking, because Apple offers no radius parameter and any such policy
// belongs to the caller rather than to a transport client.
func (c *Client) SearchAll(ctx context.Context, req SearchRequest, maxPages int) (*SearchAllResult, error) {
	if req.Q == "" {
		return nil, errors.New("applemaps: SearchAll requires Q")
	}
	if maxPages <= 0 {
		maxPages = DefaultMaxSearchPages
	}

	req.EnablePagination = true
	result := &SearchAllResult{}

	for {
		page, err := c.Search(ctx, req)
		if err != nil {
			// A failure partway through still returns what was gathered, so a
			// caller can decide between using a partial result and discarding
			// it. Losing four good pages to one transient error would be worse.
			if result.Pages > 0 {
				return result, fmt.Errorf("applemaps: search page %d: %w", result.Pages+1, err)
			}
			return nil, err
		}

		result.Pages++
		result.Places = append(result.Places, page.Results...)
		if result.Pages == 1 {
			result.DisplayMapRegion = page.DisplayMapRegion
		}

		next := ""
		if page.PaginationInfo != nil {
			next = page.PaginationInfo.NextPageToken
		}
		if next == "" {
			return result, nil
		}
		if result.Pages >= maxPages {
			result.Truncated = true
			return result, nil
		}
		req.PageToken = next
	}
}

// SearchAutocompleteRequest describes a /v1/searchAutocomplete call.
//
// ResultTypeFilter uses SearchACResultType rather than SearchResultType: this
// endpoint has no address member.
type SearchAutocompleteRequest struct {
	// Q is the partial query to complete. Required.
	Q string
	// IncludePoiCategories restricts suggestions to these categories.
	IncludePoiCategories []PoiCategory
	// ExcludePoiCategories removes these categories from suggestions.
	ExcludePoiCategories []PoiCategory
	// LimitToCountries is a list of two-letter ISO 3166-1 codes.
	LimitToCountries []string
	// ResultTypeFilter restricts which kinds of suggestion come back.
	ResultTypeFilter []SearchACResultType
	// IncludeAddressCategories requires an address result type filter.
	IncludeAddressCategories []AddressCategory
	// ExcludeAddressCategories carries the same requirement.
	ExcludeAddressCategories []AddressCategory
	// Lang overrides the client's default language. It must be set here rather
	// than appended to a returned CompletionURL, which Apple resolves in the
	// language of the original request.
	Lang string
	// SearchLocation biases suggestions toward a coordinate.
	SearchLocation *Location
	// SearchRegion biases suggestions toward a bounding box.
	SearchRegion *MapRegion
	// UserLocation is used as a fallback bias when SearchLocation is unset.
	UserLocation *Location
	// SearchRegionPriority indicates how strongly to weight SearchRegion.
	SearchRegionPriority string
}

func (r SearchAutocompleteRequest) params(c *Client) url.Values {
	params := url.Values{}
	params.Set("q", r.Q)
	setCategories(params, "includePoiCategories", r.IncludePoiCategories)
	setCategories(params, "excludePoiCategories", r.ExcludePoiCategories)
	setStrings(params, "limitToCountries", r.LimitToCountries)

	if len(r.ResultTypeFilter) > 0 {
		values := make([]string, len(r.ResultTypeFilter))
		for i, t := range r.ResultTypeFilter {
			values[i] = string(t)
		}
		setStrings(params, "resultTypeFilter", values)
	}
	setAddressCategories(params, "includeAddressCategories", r.IncludeAddressCategories)
	setAddressCategories(params, "excludeAddressCategories", r.ExcludeAddressCategories)

	c.applyLang(params, r.Lang)
	if r.SearchLocation != nil {
		params.Set("searchLocation", formatLocation(r.SearchLocation.Latitude, r.SearchLocation.Longitude))
	}
	if r.SearchRegion != nil {
		params.Set("searchRegion", formatRegion(*r.SearchRegion))
	}
	if r.UserLocation != nil {
		params.Set("userLocation", formatLocation(r.UserLocation.Latitude, r.UserLocation.Longitude))
	}
	if r.SearchRegionPriority != "" {
		params.Set("searchRegionPriority", r.SearchRegionPriority)
	}
	return params
}

// SearchAutocomplete returns suggestions for a partial query. An empty result
// set is not an error, for the same reason as Search.
func (c *Client) SearchAutocomplete(ctx context.Context, req SearchAutocompleteRequest) ([]AutocompleteResult, error) {
	if req.Q == "" {
		return nil, errors.New("applemaps: SearchAutocomplete requires Q")
	}

	var resp SearchAutocompleteResponse
	if err := c.get(ctx, searchAutocompletePath, req.params(c), &resp); err != nil {
		return nil, err
	}
	return resp.Results, nil
}
