package bananas

import (
	"regexp"

	"github.com/WedgeNix/bananas/ship"
	"github.com/WedgeNix/util"
	"github.com/WedgeNix/warehouse-settings"

	"os"

	"github.com/WedgeNix/warehouse-settings/app"
)

// RunSandbox runs Bananas® with no stateful changes.
func RunSandbox() []error {
	sandbox = true
	return Run()
}

// CreateOrdersOnly runs Bananas® only creating orders on Ship Station.
func CreateOrdersOnly() []error {
	paperless = true
	dontEmailButCreateOrders = true
	return Run()
}

// RunPaperless runs Bananas® without emailing anybody.
func RunPaperless() []error {
	paperless = true
	return Run()
}

// Run initializes all package-level variables.
func Run() []error {
	if !sandbox && hit && !paperless {
		util.Log("bananas already hit; we don't want to re-email everyone")
		return []error{nil}
	}

	// pull environmental variables
	shipKey = os.Getenv("SHIP_API_KEY")
	shipSecret = os.Getenv("SHIP_API_SECRET")
	settingsURL = os.Getenv("SETTINGS_URL")
	appUser = os.Getenv("APP_EMAIL_USER")
	appPass = os.Getenv("APP_EMAIL_PASS")

	util.Log("grab vendor settings for bananas")

	sets := app.Bananas{}
	if err := wedgenix.Settings(&sets); err != nil {
		return []error{err}
	}

	// printJSON(sets)

	// compile all vendor name expressions
	exprs := map[string]*regexp.Regexp{}
	for vend, set := range sets {
		exprs[vend] = regexp.MustCompile(set.Regex)
	}

	util.Log("initializing monitor")

	jc, errc := newJIT()
	if err := <-errc; err != nil {
		return []error{util.Err(err)}
	}

	util.Log("reading from AWS")

	j := <-jc
	rdc, errc := j.ReadAWS()
	if err := <-errc; err != nil {
		return []error{util.Err(err)}
	}

	v := Vars{
		settings:    sets,
		vendExprs:   exprs,
		j:           &j,
		login:       util.HTTPLogin{User: shipKey, Pass: shipSecret},
		hasVendor:   regexp.MustCompile(`W[0-9](-[A-Z0-9]+)+`),
		localOnly:   regexp.MustCompile(`(, *|^)[0-9A-Z]+ *\([0-9]+\)`),
		quantity:    regexp.MustCompile(`[0-9]+(?:\))`),
		number:      regexp.MustCompile(`[0-9]+`),
		inWarehouse: map[string]int{},
		toBeTagged:  map[int]bool{},
		vendors:     map[string]string{},
		taggables:   []ship.Order{},
		errs:        []error{},
	}

	util.Log("get orders that are awaiting shipment")

	pay, err := v.getOrdersAwaitingShipment()
	if err != nil {
		return v.err(err)
	}
	v.original = pay.preserveItems()

	util.Log("filter the orders for drop ship only (except monitors)")

	filteredPay, upc, errc := v.filterDropShipment(pay, rdc)
	if !dontEmailButCreateOrders {
		if err = <-errc; err != nil {
			return v.err(err)
		}
	}

	util.Log("arrange the orders based on time-preference grading")

	arrangedPay, errs := v.arrangeOrders(filteredPay)
	v.err(errs...)
	log(v.inWarehouse, "^^^ inWarehouse (on hand currently)")

	util.Log("convert to stateful for in-order ship.Item quantities")

	bans, err := v.statefulConversion(arrangedPay)
	if err != nil {
		return v.err(err)
	}

	util.Log("place higher needed quantities on top for emails")

	util.Log("DROP SHIP:")
	bans = bans.sort().print()

	//
	// Honestly stateful stuff (real world)
	// ||||||||
	// vvvvvvvv

	util.Log("email the respective orders")

	taggableBans, err := v.emailOrders(bans)
	if err != nil {
		return v.err(err)
	}

	util.Log("tag the orders on ShipStation")

	err = v.tagAndUpdate(taggableBans)
	if err != nil {
		return v.err(err)
	}

	if !sandbox && !dsOnly && !dontEmailButCreateOrders {
		util.Log("save config file on AWS")
		errc = v.j.SaveAWSChanges(upc)
		if err = <-errc; err != nil {
			return v.err(err)
		}
	} else {
		println("[not saving monitor file(s)]")
	}

	hit = true

	return v.errs
}
