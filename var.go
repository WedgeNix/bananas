package bananas

var (
	// shipKey holds the API key for accessing ShipStation.
	shipKey string

	// shipSecret holds the ShipStation API secret to be paired with the key.
	shipSecret string

	// settingsURL is the cloud location for all vendor warehouse settings.
	settingsURL string

	// vars for email authentication
	comEmailUser string
	comEmailPass string
	comEmailSMTP string
	appUser      string
	appPass      string

	// hit intended to exist in memory until the controlling mux dies
	hit                      bool
	paperless                bool
	dontEmailButCreateOrders bool
)
