package applemaps

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestSearchEncodesParams(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, eiffelTowerSearchResponse)
	})

	resp, err := client.Search(context.Background(), SearchRequest{
		Q:                        "supermarket",
		IncludePoiCategories:     []PoiCategory{PoiCategoryFoodMarket, PoiCategoryStore},
		ExcludePoiCategories:     []PoiCategory{PoiCategoryGasStation},
		LimitToCountries:         []string{"US"},
		ResultTypeFilter:         []SearchResultType{SearchResultTypePoi, SearchResultTypeAddress},
		IncludeAddressCategories: []AddressCategory{AddressCategoryLocality, AddressCategoryPostalCode},
		ExcludeAddressCategories: []AddressCategory{AddressCategoryCountry},
		SearchLocation:           &Location{Latitude: 37.78, Longitude: -122.42},
		SearchRegion:             &MapRegion{NorthLatitude: 38, EastLongitude: -122.1, SouthLatitude: 37.5, WestLongitude: -122.5},
		UserLocation:             &Location{Latitude: 37.7, Longitude: -122.4},
		SearchRegionPriority:     "required",
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if gotPath != searchPath {
		t.Errorf("path: got %q, want %q", gotPath, searchPath)
	}
	checks := map[string]string{
		"q":                        "supermarket",
		"includePoiCategories":     "FoodMarket,Store",
		"excludePoiCategories":     "GasStation",
		"limitToCountries":         "US",
		"resultTypeFilter":         "poi,address",
		"includeAddressCategories": "Locality,PostalCode",
		"excludeAddressCategories": "Country",
		"searchLocation":           "37.78,-122.42",
		"searchRegion":             "38,-122.1,37.5,-122.5",
		"userLocation":             "37.7,-122.4",
		"searchRegionPriority":     "required",
	}
	for key, want := range checks {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("%s: got %q, want %q", key, got, want)
		}
	}

	// Search must not set pagination on its own; only SearchAll does.
	if _, present := gotQuery["enablePagination"]; present {
		t.Error("enablePagination should be absent unless requested")
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results: got %d, want 1", len(resp.Results))
	}
	if resp.Results[0].PoiCategory != PoiCategoryLandmark {
		t.Errorf("poiCategory: got %q, want Landmark", resp.Results[0].PoiCategory)
	}
}

// An area with none of the requested kind of place is a real answer, not a
// failure. Callers scanning a grid need to tell that apart from an error.
func TestSearchEmptyResultsIsNotAnError(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"results":[]}`)
	})

	resp, err := client.Search(context.Background(), SearchRequest{Q: "ski slope"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Errorf("results: got %d, want 0", len(resp.Results))
	}
}

func TestSearchRequiresQ(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without Q")
	})
	if _, err := client.Search(context.Background(), SearchRequest{}); err == nil {
		t.Error("want an error when Q is empty")
	}
	if _, err := client.SearchAll(context.Background(), SearchRequest{}, 3); err == nil {
		t.Error("SearchAll should also require Q")
	}
}

// pagedSearchServer serves numbered pages, handing out a next token until the
// last one.
func pagedSearchServer(t *testing.T, totalPages int) (*Client, *atomic.Int64, *[]string) {
	t.Helper()
	var calls atomic.Int64
	tokensSeen := make([]string, 0, totalPages)

	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		tokensSeen = append(tokensSeen, r.URL.Query().Get("pageToken"))

		next := ""
		if n < totalPages {
			next = fmt.Sprintf("token-%d", n+1)
		}
		fmt.Fprintf(w, `{
			"displayMapRegion":{"northLatitude":1,"eastLongitude":2,"southLatitude":3,"westLongitude":4},
			"results":[{"name":"place-%d","coordinate":{"latitude":1,"longitude":2}}],
			"paginationInfo":{"nextPageToken":%q,"totalPageCount":%d,"totalResults":%d}
		}`, n, next, totalPages, totalPages)
	})
	return client, &calls, &tokensSeen
}

func TestSearchAllFollowsPagination(t *testing.T) {
	client, calls, tokensSeen := pagedSearchServer(t, 3)

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 10)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}

	if got := calls.Load(); got != 3 {
		t.Errorf("requests: got %d, want 3", got)
	}
	if result.Pages != 3 {
		t.Errorf("pages: got %d, want 3", result.Pages)
	}
	if len(result.Places) != 3 {
		t.Fatalf("places: got %d, want 3", len(result.Places))
	}
	if result.Truncated {
		t.Error("truncated should be false when pagination ran to completion")
	}
	// The first page carries no token; each later page must send the token the
	// previous response supplied.
	want := []string{"", "token-2", "token-3"}
	for i, w := range want {
		if (*tokensSeen)[i] != w {
			t.Errorf("page %d pageToken: got %q, want %q", i+1, (*tokensSeen)[i], w)
		}
	}
	if result.DisplayMapRegion == nil || result.DisplayMapRegion.NorthLatitude != 1 {
		t.Error("displayMapRegion should come from the first page")
	}
}

func TestSearchAllSetsEnablePagination(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"results":[]}`)
	})

	if _, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 3); err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if got := gotQuery.Get("enablePagination"); got != "true" {
		t.Errorf("enablePagination: got %q, want true", got)
	}
}

func TestSearchAllStopsWhenNoNextToken(t *testing.T) {
	client, calls, _ := pagedSearchServer(t, 1)

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 10)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("requests: got %d, want 1", got)
	}
	if result.Truncated {
		t.Error("truncated should be false")
	}
}

// Silently returning a short list reads as "that is all there is". Truncation
// must be visible.
func TestSearchAllReportsTruncation(t *testing.T) {
	client, calls, _ := pagedSearchServer(t, 10)

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 3)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("requests: got %d, want 3 — the page cap must be honoured", got)
	}
	if !result.Truncated {
		t.Error("truncated should be true when the cap was hit with pages remaining")
	}
	if len(result.Places) != 3 {
		t.Errorf("places: got %d, want 3", len(result.Places))
	}
}

func TestSearchAllDefaultsPageCap(t *testing.T) {
	client, calls, _ := pagedSearchServer(t, 100)

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 0)
	if err != nil {
		t.Fatalf("SearchAll: %v", err)
	}
	if got := calls.Load(); got != int64(DefaultMaxSearchPages) {
		t.Errorf("requests: got %d, want %d", got, DefaultMaxSearchPages)
	}
	if !result.Truncated {
		t.Error("truncated should be true")
	}
}

// Losing several good pages because the next one failed would be worse than
// handing back a partial result and saying so.
func TestSearchAllReturnsPartialResultOnLaterPageFailure(t *testing.T) {
	var calls atomic.Int64
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			fmt.Fprint(w, `{"results":[{"name":"first"}],"paginationInfo":{"nextPageToken":"t2"}}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"bad token"}`)
	})

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 5)
	if err == nil {
		t.Fatal("want an error reporting the failed page")
	}
	if result == nil {
		t.Fatal("want the partial result alongside the error")
	}
	if len(result.Places) != 1 || result.Places[0].Name != "first" {
		t.Errorf("places: got %+v, want the one page that succeeded", result.Places)
	}
}

func TestSearchAllFirstPageFailureReturnsNoResult(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"message":"nope"}`)
	})

	result, err := client.SearchAll(context.Background(), SearchRequest{Q: "cafe"}, 5)
	if err == nil {
		t.Fatal("want an error")
	}
	if result != nil {
		t.Errorf("want nil result when nothing was gathered, got %+v", result)
	}
}

func TestSearchAutocomplete(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"results":[
			{"completionUrl":"/v1/search?q=eiffel&metadata=abc",
			 "displayLines":["Eiffel Tower","Paris, France"],
			 "location":{"latitude":48.858,"longitude":2.294},
			 "structuredAddress":{"locality":"Paris"}}
		]}`)
	})

	results, err := client.SearchAutocomplete(context.Background(), SearchAutocompleteRequest{
		Q:                "eiffel",
		ResultTypeFilter: []SearchACResultType{SearchACResultTypePoi, SearchACResultTypePhysicalFeature},
		LimitToCountries: []string{"FR"},
		SearchLocation:   &Location{Latitude: 48.85, Longitude: 2.29},
	})
	if err != nil {
		t.Fatalf("SearchAutocomplete: %v", err)
	}

	if gotPath != searchAutocompletePath {
		t.Errorf("path: got %q, want %q", gotPath, searchAutocompletePath)
	}
	if got, want := gotQuery.Get("resultTypeFilter"), "poi,physicalFeature"; got != want {
		t.Errorf("resultTypeFilter: got %q, want %q", got, want)
	}
	if got := gotQuery.Get("q"); got != "eiffel" {
		t.Errorf("q: got %q", got)
	}

	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}
	got := results[0]
	if got.CompletionURL != "/v1/search?q=eiffel&metadata=abc" {
		t.Errorf("completionUrl: got %q", got.CompletionURL)
	}
	if len(got.DisplayLines) != 2 {
		t.Errorf("displayLines: got %d, want 2", len(got.DisplayLines))
	}
	if got.Location == nil || got.Location.Latitude != 48.858 {
		t.Error("location did not decode")
	}
	if got.StructuredAddress == nil || got.StructuredAddress.Locality != "Paris" {
		t.Error("structuredAddress did not decode")
	}
}

func TestSearchAutocompleteRequiresQ(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without Q")
	})
	if _, err := client.SearchAutocomplete(context.Background(), SearchAutocompleteRequest{}); err == nil {
		t.Error("want an error when Q is empty")
	}
}

func TestSearchPropagatesQuotaError(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"message":"Quota exceeded"}`)
	})

	_, err := client.Search(context.Background(), SearchRequest{Q: "cafe"})
	var quotaErr *QuotaError
	if !errors.As(err, &quotaErr) {
		t.Fatalf("got %T (%v), want *QuotaError", err, err)
	}
}
