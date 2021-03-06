package bananas

import (
	"strconv"
	"strings"
	"time"

	"github.com/WedgeNix/util"
)

var (
	contains = strings.Contains
	log      = util.Log

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
	sandbox                  bool
	hit                      bool
	paperless                bool
	dontEmailButCreateOrders bool

	la, _ = time.LoadLocation("America/Los_Angeles")
	itoa  = strconv.Itoa
)
