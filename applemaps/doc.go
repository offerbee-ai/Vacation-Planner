// Package applemaps is a client for the Apple Maps Server API
// (https://maps-api.apple.com).
//
// The package is deliberately free of any dependency on the rest of this
// repository — it knows nothing about POI, iowrappers, or Redis — so it can be
// extracted into its own module without a rewrite. Mapping Apple's types onto
// this service's domain model is the job of the adapter in iowrappers, not of
// this package.
//
// Authentication is a two-hop exchange. A caller supplies an Apple Developer
// team ID, a MapKit key ID, and the ECDSA private key from the corresponding
// .p8 file; the client signs a short-lived ES256 JWT with them, exchanges it at
// /v1/token for an access token, and sends that token on every subsequent call.
// TokenSource handles the caching and refresh of that access token.
//
// Apple enforces a quota of 25,000 calls per day per developer team, shared with
// MapKit JS, and returns HTTP 429 on every endpoint once it is exhausted. The
// client surfaces that as *QuotaError so callers can route around it rather than
// treating it as a generic failure.
package applemaps
