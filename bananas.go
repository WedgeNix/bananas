package bananas

import (
	"encoding/csv"
	"errors"
	"sort"
	"strconv"
	"sync"

	"regexp"

	"math"

	"strings"

	"time"

	"github.com/WedgeNix/util"
	"github.com/gin-gonic/gin"

	"bytes"
	"html/template"
	"net/http"
	"os"

	"io/ioutil"

	"encoding/json"

	"github.com/mrmiguu/print"
	"github.com/mrmiguu/un"
)

const (
	// removes real world side effects to testing purposes
	sandbox    = true
	paperless  = false
	ignoreCF1  = false
	monitoring = false

	// shipURL is the http location for API calls.
	shipURL = "https://ssapi.shipstation.com/"
)

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
	hit bool
)

// Monitor JUST FOR TESTING NEED TO REPLACEW
type Monitor struct {
	Pending bool
}

// Vars is the bananas data structure.
type Vars struct {
	context *gin.Context

	// settings holds all static vendor warehouse information.
	settings map[string]vendorSetting

	j *jit

	login util.HTTPLogin

	// hasVendor checks a warehouse location string to see if the item exists in quantity on their end.
	hasVendor *regexp.Regexp
	// vendorOnly picks out vendor-only warehouse locations with quantities.
	// vendorOnly *regexp.Regexp
	// localOnly picks out local-only warehouse locations with quantities.
	localOnly *regexp.Regexp
	// quantity grabs the value within a single warehouse location.
	quantity *regexp.Regexp
	// number is just a series of numbers.
	number *regexp.Regexp

	// inWarehouse keeps track of quantities of on-hand goods based on SKU/UPC.
	inWarehouse map[string]int

	// broken tracks orders needing complete order requests.
	broken map[int]bool

	// vendors holds an instant mapping of warehouse locations to vendor names.
	vendors map[string]string

	// taggables
	taggables []order

	// holds onto errors that need to be handled later for damage control
	errs []error
}

type vendorSetting struct {
	Location      string
	Email         []string
	PONum         string
	ShareOffPrice bool
	WaitingPeriod int
	FileDownload  bool
	UseUPC        bool
	Monitor       bool
}

func (v *Vars) err(err ...error) []error {
	v.errs = append(v.errs, err...)
	return v.errs
}

// Run initializes all package-level variables.
func Run(c *gin.Context) []error {
	var errs []error

	if hit && !paperless {
		return append(errs, errors.New("bananas already hit; we don't want to re-email everyone"))
	}

	// pull environmental variables
	shipKey = os.Getenv("SHIP_API_KEY")
	shipSecret = os.Getenv("SHIP_API_SECRET")
	settingsURL = os.Getenv("SETTINGS_URL")
	settingsUser := os.Getenv("SETTINGS_USER")
	settingsPass := os.Getenv("SETTINGS_PASS")

	comEmailUser = os.Getenv("COM_EMAIL_USER")
	comEmailPass = os.Getenv("COM_EMAIL_PASS")
	comEmailSMTP = os.Getenv("COM_EMAIL_SMTP")
	appUser = os.Getenv("APP_EMAIL_USER")
	appPass = os.Getenv("APP_EMAIL_PASS")

	// program-based settings
	print.Goroutines(false)

	print.Debug("grab vendor settings for bananas")

	req, err := http.NewRequest(http.MethodGet, settingsURL, nil)
	if err != nil {
		return append(errs, err)
	}
	req.Header.Add("User", settingsUser)
	req.Header.Add("Pass", settingsPass)

	cl := http.Client{}
	resp, err := cl.Do(req)
	if err != nil {
		return append(errs, err)
	}
	defer resp.Body.Close()

	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return append(errs, err)
	}

	print.Debug("convert settings file into our matching structure")

	settings := map[string]vendorSetting{}
	err = json.Unmarshal(b, &settings)
	if err != nil {
		return append(errs, err)
	}

	printJSON(settings)

	j, err := newJIT()
	if err != nil {
		return append(errs, err)
	}

	v := Vars{
		context:     c,
		settings:    settings,
		j:           j,
		login:       util.HTTPLogin{User: shipKey, Pass: shipSecret},
		hasVendor:   regexp.MustCompile(`W[0-9](-[A-Z0-9]+)+`),
		localOnly:   regexp.MustCompile(`(, *|^)[0-9A-Z]+ *\([0-9]+\)`),
		quantity:    regexp.MustCompile(`[0-9]+(?:\))`),
		number:      regexp.MustCompile(`[0-9]+`),
		inWarehouse: map[string]int{},
		broken:      map[int]bool{},
		vendors:     map[string]string{},
		taggables:   []order{},
		errs:        errs,
	}

	print.Debug("get orders that are awaiting shipment")

	pay, err := v.getOrdersAwaitingShipment()
	if err != nil {
		return v.err(err)
	}

	print.Debug("filter the orders for drop ship only (except monitors)")

	filteredPay := v.filterDropShipment(pay)

	print.Debug("arrange the orders based on time-preference grading")

	arrangedPay, errs := v.arrangeOrders(filteredPay)
	v.err(errs...)

	print.Debug("convert to stateful for in-order item quantities")

	bans := v.statefulConversion(arrangedPay)

	print.Debug("place higher needed quantities on top for emails")

	sortedBans := bans.sort().print()

	print.Debug("email the respective orders")

	taggableBans, err := v.order(sortedBans)
	if err != nil {
		return v.err(err)
	}

	print.Debug("tag the orders on ShipStation")

	v.tagAndUpdate(taggableBans)

	return v.errs
}

func printJSON(v interface{}) {
	print.Msg(string(un.Bytes(json.MarshalIndent(v, "", "    "))))
}

// // GetOrdersAwaitingShipment grabs an HTTP response of orders, filtering in those awaiting shipment.
func (v *Vars) getOrdersAwaitingShipment() (*payload, error) {
	pg := 1

	pay := &payload{}
	reqs, secs, err := v.getPage(pg, pay)
	if err != nil {
		return pay, err
	}

	for pay.Page < pay.Pages {
		pg++
		ords := pay.Orders

		pay = &payload{}
		reqs, secs, err = v.getPage(pg, pay)
		if err != nil {
			return pay, err
		}
		if reqs < 1 {
			time.Sleep(time.Duration(secs) * time.Second)
		}

		pay.Orders = append(ords, pay.Orders...)
	}

	return pay, nil
}

func (v *Vars) getPage(page int, pay *payload) (int, int, error) {
	last := time.Now().UTC().String()  // to go into AWS
	today := time.Now().UTC().String() // to go into AWS

	query := `orders?page=` + strconv.Itoa(page) + `
	&orderDateStart=` + last + `
	&orderDateEnd=` + today + `
	&pageSize=500`

	resp := v.login.Get(shipURL + query)
	err := json.NewDecoder(resp.Body).Decode(pay)
	defer resp.Body.Close()

	reqs, err := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Remaining"))
	if err != nil {
		return 0, 0, err
	}
	secs, err := strconv.Atoi(resp.Header.Get("X-Rate-Limit-Reset"))
	if err != nil {
		return reqs, 0, err
	}

	return reqs, secs, err
}

// Payload is the first level of a ShipStation HTTP response body.
type payload struct {
	Orders []order
	Total  int
	Page   int
	Pages  int
}

// FilterDropShipment scans all items of all orders looking for drop ship candidates.
func (v *Vars) filterDropShipment(pay *payload) filteredPayload {

	print.Debug("copy payload and overwrite copy's orders")

	ords := pay.Orders
	pay.Orders = []order{}

	print.Debug("go through all orders")

OrderLoop:
	for _, ord := range ords {
		// cf3 := ord.AdvancedOptions.CustomField3

		// if strings.Contains(cf3, "FAILED:") {

		// }

		// cf3 = strings.Replace(ord.AdvancedOptions.CustomField3, "FAILED:", "", -1)
		// 	for _, vend := range strings.Split(cf3, `;`) {
		// 	}

		if strings.ContainsAny(ord.AdvancedOptions.CustomField1, "Vv") && !(sandbox && ignoreCF1) {
			// print.Msg("next order... [cf1] => ", ord.AdvancedOptions.CustomField1)
			continue OrderLoop
		}

		items := ord.Items
		ord.Items = []item{}
		for _, itm := range items {
			monitor := false
			// for vend, setting := range v.settings {
			// 	if !(setting.Monitor && itemVendMatch(itm, vend)) {
			// 		continue
			// 	}
			// 	monitor = true
			// 	break
			// }
			w2 := v.hasVendor.MatchString(itm.WarehouseLocation)
			// to continue, must be W2 or monitored
			if !(w2 || monitor) {
				continue
			}
			// if monitor {
			// 	print.Msg("FOUND a monitor")
			// 	print.Msg(itm)
			// 	// monSku = itm.SKU
			// }

			ord.Items = append(ord.Items, itm)
		}

		if len(ord.Items) < 1 {
			continue
		}

		pay.Orders = append(pay.Orders, ord)
	}

	return filteredPayload(*pay)
}

// Print prints the payload in a super minimal format.
func (v *Vars) print(p payload) error {
	for oi, o := range p.Orders {
		for ii, i := range o.Items {
			abcdf, err := o.grade(v)
			if err != nil {
				return err
			}
			// fmt.Printf("%d/%d ~ %d/%d : %dx : %v | %s; %s\n", oi+1, cap(p.Orders), ii+1, cap(o.Items), i.Quantity, o.grade(v), v.skupc(i), i.WarehouseLocation)
			print.Msg(
				oi+1, "/", cap(p.Orders),
				" ~ ",
				ii+1, "/", cap(o.Items),
				" : ",
				i.Quantity,
				" : ",
				abcdf,
				" | ",
				v.skupc(i), "; ",
				i.WarehouseLocation,
			)
		}
	}
	return nil
}

// FilteredPayload is a type-safe payload already filtered.
type filteredPayload payload

// ArrangeOrders goes through and sorts/ranks orders based on best fulfillment.
func (v *Vars) arrangeOrders(f filteredPayload) (arrangedPayload, []error) {
	errs := []error{}
	sort.Slice(f.Orders, func(i, j int) bool {
		iAbcdf, err := f.Orders[i].grade(v)
		if err != nil {
			errs = append(errs, err)
		}
		jAbcdf, err := f.Orders[j].grade(v)
		if err != nil {
			errs = append(errs, err)
		}
		return iAbcdf < jAbcdf
	})
	return arrangedPayload(f), errs
}

// ArrangedPayload is a type-safe payload already arranged.
type arrangedPayload payload

// StatefulConversion maps from stateless item information to in-memory stateful representation.
func (v *Vars) statefulConversion(arrangedPay arrangedPayload) bananas {
	bans := bananas{}
	innerBroke := []order{}

	print.Debug("run over all orders/items")

	for _, ord := range arrangedPay.Orders {
		var savedO *order
		for _, i := range ord.Items {
			if v.broken[ord.OrderID] {
				v.add(bans, &i)
				savedO = &ord
			} else if v.inWarehouse[v.skupc(i)]-i.Quantity < 0 {
				innerBroke = append(innerBroke, ord)
			} else {
				v.inWarehouse[v.skupc(i)] -= i.Quantity
			}
		}
		if savedO != nil {
			v.taggables = append(v.taggables, *savedO)
		}
	}

	print.Debug("hybrid orders that broke during stateful conversion")

	for _, ord := range innerBroke {
		for _, i := range ord.Items {
			v.add(bans, &i)
		}
		v.taggables = append(v.taggables, ord)
	}

	print.Debug("before leaving, 'order' monitored quantities of zero")

	// print.Msg("monSku: ", monSku)
	// print.Msg("onHand: ", v.inWarehouse[monSku])

	for _, ord := range arrangedPay.Orders {
		for _, itm := range ord.Items {
			for vend, setting := range v.settings {
				if !setting.Monitor || !itemVendMatch(itm, vend) || v.inWarehouse[v.skupc(itm)] != 0 {
					continue
				}

				print.Debug("a vendor-item to be bananafied and added")

				ban := banana{v.skupc(itm), 1}
				v.addBan(bans, vend, ban)
			}
		}
	}

	return bans
}

func itemVendMatch(i item, vend string) bool {
	return strings.Contains(i.Name, vend)
}

// Banana is a single order to be requested for a vendor.
type banana struct {
	SKUPC    string
	Quantity int
}

// Bunch is a slice of bananas.
type bunch []banana

// bananas maps a vendor name to a SKU to a requested quantity.
type bananas map[string]bunch

// add places an item based on vendor and SKU into the banana bunch.
func (v *Vars) add(bans bananas, i *item) {
	v.addBan(bans, v.toVendor(i.Name), banana{v.skupc(*i), i.Quantity})
}

func (v *Vars) addBan(bans bananas, vend string, ban banana) {
	_, exists := bans[vend]
	if !exists {
		bans[vend] = bunch{}
	}

	// copy
	newBunch := bans[vend]
	found := false

	// go through and see if we're adding to existing quantity (in email)
	for i, bana := range newBunch {
		if bana.SKUPC == ban.SKUPC {
			newBunch[i].Quantity += ban.Quantity
			found = true
		}
	}

	if !found {
		newBunch = append(newBunch, ban)
	}

	// new bunch
	bans[vend] = newBunch
}

// Print prints the hierarchy of the banana bunch.
func (b bananas) print() bananas {
	for k, v := range b {
		print.Msg(k, ":")
		for ik, iv := range v {
			print.Msg("\t", ik, ": ", iv)
		}
	}
	print.Msg()
	return b
}

// Sort sorts bunches within the bananas, placing higher quantities on top
func (b bananas) sort() bananas {
	for _, bunch := range b {
		sort.Slice(bunch, func(i, j int) bool {
			return bunch[i].Quantity > bunch[j].Quantity
		})
	}
	return b
}

// CSV writes a bunch of bananas into a comma-separated file, returning the name.
func (b bunch) csv(name string) string {

	print.Debug("create .csv for email attachment")

	var f os.File
	un.Known(os.Create(name + ".csv")).T(&f)
	defer f.Close()

	csv := csv.NewWriter(&f)
	un.Wrap(csv.Write([]string{"SKU/UPC", "Quantity"}))

	for _, banana := range b {
		un.Wrap(csv.Write([]string{banana.SKUPC, strconv.Itoa(banana.Quantity)}))
	}

	csv.Flush()
	return f.Name()
}

func (v *Vars) removeMonitors(bans bananas) {
	// for vend, setting := range v.settings {
	// 	if !setting.Monitor {
	// 		continue
	// 	}
	// 	day := time.Now().Weekday()
	// 	if day == time.Monday || day == time.Thursday {
	// 		continue
	// 	}
	// 	delete(bans, vend)
	// }
}

// Order sends email orders out to vendors for drop shipment product.
func (v *Vars) order(b bananas) (taggableBananas, error) {

	print.Debug("removing monitors from emails if not M/Th")

	v.removeMonitors(b)

	print.Debug("parse HTML template")

	var tmpl template.Template
	un.Known(template.ParseFiles("vendor-email-tmpl.html")).T(&tmpl)

	login := util.EmailLogin{
		User: comEmailUser,
		Pass: comEmailPass,
		SMTP: comEmailSMTP,
	}

	if sandbox {
		login.User = appUser
		login.Pass = appPass
	}

	var emailing sync.WaitGroup
	start := time.Now()

	var errMux sync.Mutex
	errCh := make(chan error, 1)

	for V, setting := range v.settings {
		if setting.Monitor {
			continue
		}

		B, exists := b[V]
		if !exists {

			print.Debug("send empty email")

		}
		vendor, bunch := V, B // new "variables" for closure

		emailing.Add(1)
		go func() {

			print.Debug("goroutine is starting to email a vendor")

			t := time.Now()
			po := v.settings[vendor].PONum + "-" + t.Format("20060102")

			inj := injection{
				Vendor: vendor,
				Date:   t.Format("01/02/2006"),
				PO:     po,
				Bunch:  bunch,
			}

			buf := &bytes.Buffer{}
			err := tmpl.Execute(buf, inj)
			if err != nil {
				errCh <- err
			}

			to := []string{login.User}
			if !sandbox {
				to = append(v.settings[vendor].Email, to...)
			}

			attachment := ""
			if v.settings[vendor].FileDownload && len(bunch) > 0 {
				attachment = bunch.csv(vendor)
			}

			if !paperless {
				attempts := 0
				for {
					err := login.Email(to, "WedgeNix PO#: "+po, buf.String(), attachment)
					attempts++
					if err != nil {
						if attempts <= 3 {
							print.Msg("Failed to send email. [retrying]")
							t := time.Duration(3 * attempts)
							time.Sleep(t * time.Second)
							continue
						} else {
							print.Msg("Failed to send email! [FAILED]")
							errMux.Lock()
							v.err(errors.New("failed to email " + vendor))
							errMux.Unlock()
							break
						}
					}
					break
				}
			}

			print.Debug("goroutine is finished emailing an email")

			emailing.Done()
		}()

		select {
		case err := <-errCh:
			return taggableBananas(b), err
		default:
		}
	}

	print.Debug("wait for goroutines to finish emailing")

	emailing.Wait()
	print.Msg("Emailing round-trip: ", time.Since(start))

	return taggableBananas(b), nil
}

// TagAndUpdate tags all banana orders.
func (v *Vars) tagAndUpdate(b taggableBananas) error {
	print.Msg("TAGGABLE_COUNT: ", len(v.taggables))
	tagged := make([]orderUpdate, len(v.taggables))

	for i, t := range v.taggables {
		var vSlice []string
		var upcSlice []string
		tagged[i] = orderUpdate{
			OrderNumber: t.OrderNumber,
			OrderDate:   t.OrderDate,
			OrderStatus: t.OrderStatus,
			OrderKey:    t.OrderKey,
			ShipTo:      t.ShipTo,
			BillTo:      t.BillTo,
			AdvancedOptions: advancedOptionsUpdate{
				StoreID:      t.AdvancedOptions.StoreID,
				CustomField1: t.AdvancedOptions.CustomField1,
				CustomField2: t.AdvancedOptions.CustomField2,
				CustomField3: t.AdvancedOptions.CustomField3,
			},
		}
		for _, item := range t.Items {
			vSlice = append(vSlice, v.poNum(&item, time.Now()))
			upcSlice = append(upcSlice, item.UPC)
		}
		vList := onlyUnique(vSlice)
		upcList := onlyUnique(upcSlice)
		v.taggables[i].AdvancedOptions.CustomField1 += vList
		v.taggables[i].AdvancedOptions.CustomField2 += upcList
	}

	if len(v.taggables) > 0 && !sandbox {
		resp := v.login.Post(shipURL+`orders/createorders`, v.taggables)
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		print.Msg("CREATEORDERS_RESP: ", string(b))
	}

	if sandbox {
		print.Msg("NEW_TAGGABLES: ", v.taggables)
	}

	return nil
}

// onlyUnique takes out duplicates for tagging orders.
func onlyUnique(s []string) string {
	unique := make(map[string]bool, len(s))
	unSlice := make([]string, len(unique))
	for _, po := range s {
		if !unique[po] {
			unSlice = append(unSlice, po)
			unique[po] = true
		}
	}
	return strings.Join(unSlice, ",")
}

// Taggablebananas is ready-to-be-tagged bananas.
type taggableBananas bananas

// Item is the third level of a ShipStation HTTP response body.
type item struct {
	OrderItemID       int
	LineItemKey       string
	SKU               string
	Name              string
	ImageURL          string
	Weight            interface{}
	Quantity          int
	UnitPrice         float32
	TaxAmount         float32
	ShippingAmount    float32
	WarehouseLocation string
	Options           interface{}
	ProductID         int
	FulfillmentSKU    string
	Adjustment        bool
	UPC               string
	CreateDate        string
	ModifyDate        string
}

// Grade grades the quantity requested versus what is in stock.
func (i item) grade(v *Vars) (float64, error) {
	skupc := v.skupc(i)
	onHand, exists := v.inWarehouse[skupc]
	if !exists {
		onHand, err := v.quantities(i.WarehouseLocation)
		if err != nil {
			return 0, err
		}
		v.inWarehouse[skupc] = onHand
	}
	ratio := float64(i.Quantity) / float64(onHand)
	if ratio > 1 {
		ratio = math.Inf(1)
	}
	return ratio, nil
}

// go through all vendors in the settings and return the real name if
// the location is in the item's warehouse location
//
// if for whatever reason it's not found, an empty string is returned
func (v *Vars) getVend(i item) string {
	for vend, set := range v.settings {
		if !strings.Contains(i.WarehouseLocation, set.Location) {
			continue
		}
		return vend
	}
	return ""
}

// SKUPC returns the respective SKU or UPC depending on which the vendor uses.
func (v *Vars) skupc(i item) string {
	if !v.settings[v.toVendor(i.Name)].UseUPC {
		return i.SKU
	}
	return i.UPC
}

func (v *Vars) poNum(i *item, t time.Time) string {
	return v.settings[v.toVendor(i.Name)].PONum + "-" + t.Format("20060102")
}

// Order is the second level of a ShipStation HTTP response body.
type order struct {
	OrderID                  int
	OrderNumber              string
	OrderKey                 string
	OrderDate                string
	CreateDate               string
	ModifyDate               string
	PaymentDate              string
	ShipByDate               string
	OrderStatus              string
	CustomerID               int
	CustomerUsername         string
	CustomerEmail            string
	BillTo                   interface{}
	ShipTo                   interface{}
	Items                    []item
	OrderTotal               float32
	AmountPaid               float32
	TaxAmount                float32
	ShippingAmount           float32
	CustomerNotes            string
	InternalNotes            string
	Gift                     bool
	GiftMessage              string
	PaymentMethod            string
	RequestedShippingService string
	CarrierCode              string
	ServiceCode              string
	PackageCode              string
	Confirmation             string
	ShipDate                 string
	HoldUntilDate            string
	Weight                   interface{}
	Dimensions               interface{}
	InsuranceOptions         interface{}
	InternationalOptions     interface{}
	AdvancedOptions          advancedOptions
	TagIDs                   []int
	UserID                   string
	ExternallyFulfilled      bool
	ExternallyFulfilledBy    string
}

// Grade averages all item grades within an order or gets the quantity ratio of the item.
func (o order) grade(v *Vars) (float64, error) {
	ratios := 0.0
	for _, i := range o.Items {
		abcdf, err := i.grade(v)
		if err != nil {
			return 0, err
		}
		ratios += abcdf
	}
	if ratios == math.Inf(1) {
		v.broken[o.OrderID] = true
	}
	return ratios / float64(len(o.Items)), nil
}

type orderUpdate struct {
	OrderNumber     string
	OrderKey        string
	OrderDate       string
	OrderStatus     string
	BillTo          interface{}
	ShipTo          interface{}
	AdvancedOptions advancedOptionsUpdate
}

type advancedOptionsUpdate struct {
	StoreID      int
	CustomField1 string
	CustomField2 string
	CustomField3 string
}

// AdvancedOptions holds the "needed" custom fields for post-email tagging.
type advancedOptions struct {
	WarehouseID       int
	NonMachinable     bool
	SaturdayDelivery  bool
	ContainsAlcohol   bool
	MergedOrSplit     bool
	MergedIDs         interface{}
	ParentID          interface{}
	StoreID           int
	CustomField1      string
	CustomField2      string
	CustomField3      string
	Source            string
	BillToParty       interface{}
	BillToAccount     interface{}
	BillToPostalCode  interface{}
	BillToCountryCode interface{}
}

// Injection injects structure into email template.
type injection struct {
	Vendor string
	Date   string
	PO     string
	Bunch  bunch
}

// ToVendor maps a warehouse location to a vendor name.
func (v *Vars) toVendor(title string) string {
	vend, exists := v.vendors[title]
	if exists {
		return vend
	}
	for vend := range v.settings {
		if strings.Contains(title, vend) {
			v.vendors[title] = vend
			return vend
		}
	}
	return ""
}

// quantities scans one type of warehouse in a location and sums its quantities.
func (v *Vars) quantities(s string) (int, error) {
	sum := 0
	houses := v.localOnly.FindAllString(s, -1)
	for _, house := range houses {
		quans := v.quantity.FindAllString(house, -1)
		for _, quan := range quans {
			val, err := strconv.Atoi(v.number.FindString(quan))
			if err != nil {
				return sum, err
			}
			sum += val
		}
	}
	return sum, nil
}
