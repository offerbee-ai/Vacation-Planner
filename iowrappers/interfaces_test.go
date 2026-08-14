package iowrappers

// Compile-time proof that the concrete Google client still satisfies every
// interface it is assigned to after the split. A break here is a build failure
// at the point of the mistake rather than at some distant call site.
var (
	_ Geocoder           = (*MapsClient)(nil)
	_ SearchClient       = (*MapsClient)(nil)
	_ PlaceDetailsClient = (*MapsClient)(nil)

	_ Geocoder = (*AppleMapsClient)(nil)
)
