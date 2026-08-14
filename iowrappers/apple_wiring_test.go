package iowrappers

import "testing"

// A bad key must degrade to Google-only, never take the service down. Credentials
// arrive from the environment and a deploy-time mistake is entirely possible.
func TestEnableAppleMapsWithBadCredentialsLeavesGoogleInPlace(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	err := s.EnableAppleMaps(AppleMapsSettings{
		Enabled: true, TeamID: "T", KeyID: "K", PrivateKey: "not a key",
	})
	if err == nil {
		t.Fatal("want an error for an unparseable private key")
	}
	if s.searcher != SearchClient(google) {
		t.Error("a bad key must leave the Google client in place")
	}
}

// Disabled is the default, and the router must not be constructed at all — the
// zero configuration is Google-only with nothing extra in the request path.
func TestEnableAppleMapsDisabledIsANoOp(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	if err := s.EnableAppleMaps(AppleMapsSettings{Enabled: false}); err != nil {
		t.Fatalf("EnableAppleMaps: %v", err)
	}
	if _, wrapped := s.searcher.(*AppleGeocodeRouter); wrapped {
		t.Error("Apple disabled must not wrap the searcher")
	}
}

func TestEnableAppleMapsWrapsTheSearcher(t *testing.T) {
	google := &stubSearchClient{}
	s := &PoiSearcher{searcher: google}

	if err := s.EnableAppleMaps(AppleMapsSettings{
		Enabled: true, TeamID: "TEAM123456", KeyID: "KEY7890123",
		PrivateKey: appleTestKey(t),
	}); err != nil {
		t.Fatalf("EnableAppleMaps: %v", err)
	}
	if _, wrapped := s.searcher.(*AppleGeocodeRouter); !wrapped {
		t.Error("want the searcher wrapped in an AppleGeocodeRouter")
	}
}
