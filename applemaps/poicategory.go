package applemaps

// PoiCategory describes a specific point-of-interest category.
//
// This is a closed enum defined by Apple; there is no way to request a category
// outside it. Note how coarse the retail branch is: PoiCategoryStore is the only
// general retail value, so supermarkets, shopping malls, clothing stores, and
// electronics stores are indistinguishable by category alone. PoiCategoryFoodMarket
// is the nearest thing to a grocery category and also matches specialty grocers.
type PoiCategory string

const (
	PoiCategoryAirport          PoiCategory = "Airport"
	PoiCategoryAirportGate      PoiCategory = "AirportGate"
	PoiCategoryAirportTerminal  PoiCategory = "AirportTerminal"
	PoiCategoryAmusementPark    PoiCategory = "AmusementPark"
	PoiCategoryAnimalService    PoiCategory = "AnimalService"
	PoiCategoryATM              PoiCategory = "ATM"
	PoiCategoryAutomotiveRepair PoiCategory = "AutomotiveRepair"
	PoiCategoryAquarium         PoiCategory = "Aquarium"
	PoiCategoryBakery           PoiCategory = "Bakery"
	PoiCategoryBank             PoiCategory = "Bank"
	PoiCategoryBaseball         PoiCategory = "Baseball"
	PoiCategoryBasketball       PoiCategory = "Basketball"
	PoiCategoryBeach            PoiCategory = "Beach"
	PoiCategoryBeauty           PoiCategory = "Beauty"
	PoiCategoryBowling          PoiCategory = "Bowling"
	PoiCategoryBrewery          PoiCategory = "Brewery"
	PoiCategoryCafe             PoiCategory = "Cafe"
	PoiCategoryCampground       PoiCategory = "Campground"
	PoiCategoryCarRental        PoiCategory = "CarRental"
	PoiCategoryCastle           PoiCategory = "Castle"
	PoiCategoryConventionCenter PoiCategory = "ConventionCenter"
	PoiCategoryDistillery       PoiCategory = "Distillery"
	PoiCategoryEVCharger        PoiCategory = "EVCharger"
	PoiCategoryFairground       PoiCategory = "Fairground"
	PoiCategoryFishing          PoiCategory = "Fishing"
	PoiCategoryFireStation      PoiCategory = "FireStation"
	PoiCategoryFitnessCenter    PoiCategory = "FitnessCenter"
	PoiCategoryFoodMarket       PoiCategory = "FoodMarket"
	PoiCategoryFortress         PoiCategory = "Fortress"
	PoiCategoryGasStation       PoiCategory = "GasStation"
	PoiCategoryGoKart           PoiCategory = "GoKart"
	PoiCategoryGolf             PoiCategory = "Golf"
	PoiCategoryHiking           PoiCategory = "Hiking"
	PoiCategoryHospital         PoiCategory = "Hospital"
	PoiCategoryHotel            PoiCategory = "Hotel"
	PoiCategoryKayaking         PoiCategory = "Kayaking"
	PoiCategoryLandmark         PoiCategory = "Landmark"
	PoiCategoryLaundry          PoiCategory = "Laundry"
	PoiCategoryLibrary          PoiCategory = "Library"
	PoiCategoryMailbox          PoiCategory = "Mailbox"
	PoiCategoryMarina           PoiCategory = "Marina"
	PoiCategoryMiniGolf         PoiCategory = "MiniGolf"
	PoiCategoryMovieTheater     PoiCategory = "MovieTheater"
	PoiCategoryMuseum           PoiCategory = "Museum"
	PoiCategoryMusicVenue       PoiCategory = "MusicVenue"
	PoiCategoryNationalPark     PoiCategory = "NationalPark"
	PoiCategoryNationalMonument PoiCategory = "NationalMonument"
	PoiCategoryNightlife        PoiCategory = "Nightlife"
	PoiCategoryPark             PoiCategory = "Park"
	PoiCategoryParking          PoiCategory = "Parking"
	PoiCategoryPharmacy         PoiCategory = "Pharmacy"
	PoiCategoryPlanetarium      PoiCategory = "Planetarium"
	PoiCategoryPlayground       PoiCategory = "Playground"
	PoiCategoryPolice           PoiCategory = "Police"
	PoiCategoryPostOffice       PoiCategory = "PostOffice"
	PoiCategoryPublicTransport  PoiCategory = "PublicTransport"
	PoiCategoryReligiousSite    PoiCategory = "ReligiousSite"
	PoiCategoryRestaurant       PoiCategory = "Restaurant"
	PoiCategoryRestroom         PoiCategory = "Restroom"
	PoiCategoryRockClimbing     PoiCategory = "RockClimbing"
	PoiCategoryRVPark           PoiCategory = "RVPark"
	PoiCategorySchool           PoiCategory = "School"
	PoiCategorySkatePark        PoiCategory = "SkatePark"
	PoiCategorySkating          PoiCategory = "Skating"
	PoiCategorySkiing           PoiCategory = "Skiing"
	PoiCategorySoccer           PoiCategory = "Soccer"
	PoiCategorySpa              PoiCategory = "Spa"
	PoiCategoryStadium          PoiCategory = "Stadium"
	PoiCategoryStore            PoiCategory = "Store"
	PoiCategorySurfing          PoiCategory = "Surfing"
	PoiCategorySwimming         PoiCategory = "Swimming"
	PoiCategoryTennis           PoiCategory = "Tennis"
	PoiCategoryTheater          PoiCategory = "Theater"
	PoiCategoryUniversity       PoiCategory = "University"
	PoiCategoryVolleyball       PoiCategory = "Volleyball"
	PoiCategoryWinery           PoiCategory = "Winery"
	PoiCategoryZoo              PoiCategory = "Zoo"
)

// AllPoiCategories lists every category Apple defines. Its main use is
// validating that a caller-supplied category is one Apple will accept, since an
// unknown value is rejected by the API rather than ignored.
var AllPoiCategories = []PoiCategory{
	PoiCategoryAirport, PoiCategoryAirportGate, PoiCategoryAirportTerminal,
	PoiCategoryAmusementPark, PoiCategoryAnimalService, PoiCategoryATM,
	PoiCategoryAutomotiveRepair, PoiCategoryAquarium, PoiCategoryBakery,
	PoiCategoryBank, PoiCategoryBaseball, PoiCategoryBasketball,
	PoiCategoryBeach, PoiCategoryBeauty, PoiCategoryBowling,
	PoiCategoryBrewery, PoiCategoryCafe, PoiCategoryCampground,
	PoiCategoryCarRental, PoiCategoryCastle, PoiCategoryConventionCenter,
	PoiCategoryDistillery, PoiCategoryEVCharger, PoiCategoryFairground,
	PoiCategoryFishing, PoiCategoryFireStation, PoiCategoryFitnessCenter,
	PoiCategoryFoodMarket, PoiCategoryFortress, PoiCategoryGasStation,
	PoiCategoryGoKart, PoiCategoryGolf, PoiCategoryHiking,
	PoiCategoryHospital, PoiCategoryHotel, PoiCategoryKayaking,
	PoiCategoryLandmark, PoiCategoryLaundry, PoiCategoryLibrary,
	PoiCategoryMailbox, PoiCategoryMarina, PoiCategoryMiniGolf,
	PoiCategoryMovieTheater, PoiCategoryMuseum, PoiCategoryMusicVenue,
	PoiCategoryNationalPark, PoiCategoryNationalMonument, PoiCategoryNightlife,
	PoiCategoryPark, PoiCategoryParking, PoiCategoryPharmacy,
	PoiCategoryPlanetarium, PoiCategoryPlayground, PoiCategoryPolice,
	PoiCategoryPostOffice, PoiCategoryPublicTransport, PoiCategoryReligiousSite,
	PoiCategoryRestaurant, PoiCategoryRestroom, PoiCategoryRockClimbing,
	PoiCategoryRVPark, PoiCategorySchool, PoiCategorySkatePark,
	PoiCategorySkating, PoiCategorySkiing, PoiCategorySoccer,
	PoiCategorySpa, PoiCategoryStadium, PoiCategoryStore,
	PoiCategorySurfing, PoiCategorySwimming, PoiCategoryTennis,
	PoiCategoryTheater, PoiCategoryUniversity, PoiCategoryVolleyball,
	PoiCategoryWinery, PoiCategoryZoo,
}

// Valid reports whether c is a category Apple defines.
func (c PoiCategory) Valid() bool {
	for _, known := range AllPoiCategories {
		if c == known {
			return true
		}
	}
	return false
}
