package applemaps

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestPlaceLookupByID(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"id":"ABC123","name":"Somewhere","coordinate":{"latitude":1,"longitude":2}}`)
	})

	place, err := client.Place(context.Background(), "ABC123", "en-US")
	if err != nil {
		t.Fatalf("Place: %v", err)
	}

	if want := placePath + "/ABC123"; gotPath != want {
		t.Errorf("path: got %q, want %q", gotPath, want)
	}
	if got := gotQuery.Get("lang"); got != "en-US" {
		t.Errorf("lang: got %q", got)
	}
	if place.ID != "ABC123" || place.Name != "Somewhere" {
		t.Errorf("place: got %+v", place)
	}
}

// Apple place IDs are opaque. An unescaped slash or question mark would be read
// as extra path segments or as the start of a query, silently requesting
// something else entirely.
func TestPlaceEscapesIDInPath(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"slash", "abc/def"},
		{"question mark", "abc?def"},
		{"hash", "abc#def"},
		{"space", "abc def"},
		{"percent", "abc%def"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotRawPath, gotEscaped string
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				// r.URL.Path is already decoded, so a correctly escaped id round
				// trips back to its original form as a single segment.
				gotRawPath = r.URL.Path
				gotEscaped = r.URL.EscapedPath()
				fmt.Fprint(w, `{"id":"x"}`)
			})

			if _, err := client.Place(context.Background(), tc.id, ""); err != nil {
				t.Fatalf("Place: %v", err)
			}

			if want := placePath + "/" + tc.id; gotRawPath != want {
				t.Errorf("decoded path: got %q, want %q (escaped form was %q)", gotRawPath, want, gotEscaped)
			}
		})
	}
}

func TestPlaceRequiresID(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without an id")
	})
	if _, err := client.Place(context.Background(), "", ""); err == nil {
		t.Error("want an error for an empty id")
	}
}

func TestPlacesJoinsIDs(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"results":[{"id":"a"},{"id":"b"}]}`)
	})

	resp, err := client.Places(context.Background(), []string{"a", "b", "c"}, "")
	if err != nil {
		t.Fatalf("Places: %v", err)
	}

	if gotPath != placePath {
		t.Errorf("path: got %q, want %q", gotPath, placePath)
	}
	if got, want := gotQuery.Get("ids"), "a,b,c"; got != want {
		t.Errorf("ids: got %q, want %q", got, want)
	}
	if len(resp.Results) != 2 {
		t.Errorf("results: got %d, want 2", len(resp.Results))
	}
}

// A batch lookup where some IDs fail is not a failed request. Both halves must
// reach the caller — dropping the errors hides which IDs are dead, and dropping
// the results throws away good data.
func TestPlacesSurfacesPartialSuccess(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{
			"results":[{"id":"good","name":"Real Place"}],
			"errors":[{"id":"missing","errorCode":"NOT_FOUND"},{"id":"junk","errorCode":"MALFORMED"}]
		}`)
	})

	resp, err := client.Places(context.Background(), []string{"good", "missing", "junk"}, "")
	if err != nil {
		t.Fatalf("Places: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Errorf("results: got %d, want 1", len(resp.Results))
	}
	if len(resp.Errors) != 2 {
		t.Fatalf("errors: got %d, want 2", len(resp.Errors))
	}
	if resp.Errors[0].ID != "missing" || resp.Errors[0].ErrorCode != "NOT_FOUND" {
		t.Errorf("first error: got %+v", resp.Errors[0])
	}
}

func TestPlacesRequiresIDs(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without ids")
	})
	if _, err := client.Places(context.Background(), nil, ""); err == nil {
		t.Error("want an error for no ids")
	}
	if _, err := client.Places(context.Background(), []string{}, ""); err == nil {
		t.Error("want an error for an empty id slice")
	}
}

func TestAlternateIDs(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{
			"results":[{"id":"a","alternateIds":["a1","a2"]}],
			"errors":[{"id":"b","errorCode":"NOT_FOUND"}]
		}`)
	})

	resp, err := client.AlternateIDs(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("AlternateIDs: %v", err)
	}

	if gotPath != alternateIDsPath {
		t.Errorf("path: got %q, want %q", gotPath, alternateIDsPath)
	}
	if got, want := gotQuery.Get("ids"), "a,b"; got != want {
		t.Errorf("ids: got %q, want %q", got, want)
	}
	if len(resp.Results) != 1 || len(resp.Results[0].AlternateIDs) != 2 {
		t.Errorf("results: got %+v", resp.Results)
	}
	if len(resp.Errors) != 1 {
		t.Errorf("errors: got %d, want 1", len(resp.Errors))
	}
}

// AlternateIDs takes no lang parameter; sending one would be noise.
func TestAlternateIDsSendsOnlyIDs(t *testing.T) {
	var gotQuery url.Values
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		fmt.Fprint(w, `{"results":[]}`)
	})
	client.lang = "en-US"

	if _, err := client.AlternateIDs(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("AlternateIDs: %v", err)
	}
	if len(gotQuery) != 1 {
		t.Errorf("want only ids, got %v", gotQuery)
	}
}

func TestAlternateIDsRequiresIDs(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("no request should be sent without ids")
	})
	if _, err := client.AlternateIDs(context.Background(), nil); err == nil {
		t.Error("want an error for no ids")
	}
}
