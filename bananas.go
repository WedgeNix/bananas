package bananas

import (
	"encoding/csv"
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
	sandbox    = false
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

// AwsMonitor JUST FOR TESTING NEED TO REPLACEW
type AwsMonitor map[string]Monitor

// Vars is the bananas data structure.
type Vars struct {
	context ctx

	// settings holds all static vendor warehouse information.
	settings map[string]vendorSetting

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

	failedEmails []string

	// aws *awsapi.AwsController
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

// Init initializes all package-level variables.
func Init(c *gin.Context) *Vars {
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

	context := ctx{c}
	defer context.initErrorHandler()

	// program-based settings
	print.Goroutines(false)

	print.Debug("grab vendor settings for bananas")

	var req http.Request
	un.Known(http.NewRequest(http.MethodGet, settingsURL, nil)).T(&req)

	req.Header.Add("User", settingsUser)
	req.Header.Add("Pass", settingsPass)

	cl := http.Client{}
	var resp http.Response
	un.Known(cl.Do(&req)).T(&resp)

	defer resp.Body.Close()
	b := un.Bytes(ioutil.ReadAll(resp.Body))

	print.Debug("convert settings file into our matching structure")

	settings := map[string]vendorSetting{}
	un.Wrap(json.Unmarshal(b, &settings))

	printJSON(settings)

	return &Vars{
		context:      context,
		settings:     settings,
		login:        util.HTTPLogin{User: shipKey, Pass: shipSecret},
		hasVendor:    regexp.MustCompile(`W[0-9](-[A-Z0-9]+)+`),
		localOnly:    regexp.MustCompile(`(, *|^)[0-9A-Z]+ *\([0-9]+\)`),
		quantity:     regexp.MustCompile(`[0-9]+(?:\))`),
		number:       regexp.MustCompile(`[0-9]+`),
		inWarehouse:  map[string]int{},
		broken:       map[int]bool{},
		vendors:      map[string]string{},
		taggables:    []order{},
		failedEmails: []string{},
		// aws:          awsapi.New(),
	}
}

func printJSON(v interface{}) {
	print.Msg(string(un.Bytes(json.MarshalIndent(v, "", "    "))))
}

func (c ctx) initErrorHandler() {
	err := recover()
	if err == nil {
		return
	}
	c.json(http.StatusBadRequest, "Bananas uninitialized...")
}

func (c ctx) runErrorHandler() {
	err := recover()
	if err == nil {
		hit = true
		c.json(http.StatusOK, "Bananas successful!")
		return
	}
	c.json(http.StatusBadRequest, "Bananas unsuccessful...")
}

// Run runs bananas.
func (v *Vars) Run() {
	defer v.context.runErrorHandler()

	if hit && !paperless {
		panic("bananas already hit; we don't want to re-email everyone")
	}

	print.Debug("get orders that are awaiting shipment")

	p := v.getOrdersAwaitingShipment()

	print.Debug("filter the orders for drop ship only (except monitors)")

	fp := v.filterDropShipment(p)

	print.Debug("arrange the orders based on time-preference grading")

	ap := v.arrangeOrders(fp)

	print.Debug("convert to stateful for in-order item quantities")

	b := v.statefulConversion(ap)

	print.Debug("place higher needed quantities on top for emails")

	b2 := b.sort().print()

	print.Debug("email the respective orders")

	tb := v.order(b2)

	print.Debug("tag the orders on ShipStation")

	v.tagAndUpdate(tb)
}

type ctx struct {
	context *gin.Context
}

func (c ctx) json(code int, msg string) {
	h := gin.H{
		"Status":  http.StatusText(code),
		"Message": msg,
	}
	c.context.JSON(code, h)
	print.Msg(h)
}

// // GetOrdersAwaitingShipment grabs an HTTP response of orders, filtering in those awaiting shipment.
func (v *Vars) getOrdersAwaitingShipment() *payload {
	pg := 1
	p := v.getPage(pg)
	for p.Page < p.Pages {
		pg++
		orders := p.Orders
		p = v.getPage(pg)
		p.Orders = append(orders, p.Orders...)
	}
	return p
}

func (v *Vars) getPage(page int) *payload {
	Q := `orders?page=` + strconv.Itoa(page) + `&orderStatus=awaiting_shipment&pageSize=500`
	r := v.login.Get(shipURL + Q)
	p := payload{}
	un.Wrap(json.NewDecoder(r.Body).Decode(&p))
	defer r.Body.Close()
	return &p
}

// Payload is the first level of a ShipStation HTTP response body.
type payload struct {
	Orders []order
	Total  int
	Page   int
	Pages  int
}

// FilterDropShipment scans all items of all orders looking for drop ship candidates.
func (v *Vars) filterDropShipment(p *payload) filteredPayload {

	print.Debug("copy payload and overwrite copy's orders")

	orders := p.Orders
	p.Orders = []order{}

	print.Debug("go through all orders")

OrderLoop:
	for _, ord := range orders {
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

		if len(ord.Items) > 0 { // ignore adding order if no valid items
			p.Orders = append(p.Orders, ord)
		}
	}

	return filteredPayload(*p)
}

// Print prints the payload in a super minimal format.
func (v *Vars) print(p payload) {
	for oi, o := range p.Orders {
		for ii, i := range o.Items {
			// fmt.Printf("%d/%d ~ %d/%d : %dx : %v | %s; %s\n", oi+1, cap(p.Orders), ii+1, cap(o.Items), i.Quantity, o.grade(v), v.skupc(i), i.WarehouseLocation)
			print.Msg(
				oi+1, "/", cap(p.Orders),
				" ~ ",
				ii+1, "/", cap(o.Items),
				" : ",
				i.Quantity,
				" : ",
				o.grade(v),
				" | ",
				v.skupc(i), "; ",
				i.WarehouseLocation,
			)
		}
	}
}

// FilteredPayload is a type-safe payload already filtered.
type filteredPayload payload

// ArrangeOrders goes through and sorts/ranks orders based on best fulfillment.
func (v *Vars) arrangeOrders(f filteredPayload) arrangedPayload {
	sort.Slice(f.Orders, func(i, j int) bool {
		return f.Orders[i].grade(v) < f.Orders[j].grade(v)
	})
	return arrangedPayload(f)
}

// ArrangedPayload is a type-safe payload already arranged.
type arrangedPayload payload

// StatefulConversion maps from stateless item information to in-memory stateful representation.
func (v *Vars) statefulConversion(a arrangedPayload) bananas {
	bans := bananas{}
	innerBroke := []order{}

	print.Debug("run over all orders/items")

	for _, o := range a.Orders {
		var savedO *order
		for _, i := range o.Items {
			if v.broken[o.OrderID] {
				v.add(bans, &i)
				savedO = &o
			} else if v.inWarehouse[v.skupc(i)]-i.Quantity < 0 {
				innerBroke = append(innerBroke, o)
			} else {
				v.inWarehouse[v.skupc(i)] -= i.Quantity
			}
		}
		if savedO != nil {
			v.taggables = append(v.taggables, *savedO)
		}
	}

	print.Debug("hybrid orders that broke during stateful conversion")

	for _, o := range innerBroke {
		for _, i := range o.Items {
			v.add(bans, &i)
		}
		v.taggables = append(v.taggables, o)
	}

	print.Debug("before leaving, 'order' monitored quantities of zero")

	// print.Msg("monSku: ", monSku)
	// print.Msg("onHand: ", v.inWarehouse[monSku])

	for _, ord := range a.Orders {
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
func (v *Vars) order(b bananas) taggableBananas {

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
			po := util.S(v.settings[vendor].PONum, "-", t.Format("20060102"))

			inj := injection{
				Vendor: vendor,
				Date:   t.Format("01/02/2006"),
				PO:     po,
				Bunch:  bunch,
			}

			buf := &bytes.Buffer{}
			un.Wrap(tmpl.Execute(buf, inj))

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
							// v.failedEmails = append(v.failedEmails, vendor)
							v.context.json(http.StatusBadRequest, "Bananas unsuccessful...")
							break
						}
					}
					break
				}
			}

			print.Debug("goroutine is finished emailing an email")

			emailing.Done()
		}()
	}

	print.Debug("wait for goroutines to finish emailing")

	emailing.Wait()
	print.Msg("Emailing round-trip: ", time.Since(start))

	return taggableBananas(b)
}

// TagAndUpdate tags all banana orders.
func (v *Vars) tagAndUpdate(b taggableBananas) {
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
		print.Msg("CREATEORDERS_RESP: ", util.Read(resp.Body))
	}

	if sandbox {
		print.Msg("NEW_TAGGABLES: ", v.taggables)
	}
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
func (i item) grade(v *Vars) float64 {
	skupc := v.skupc(i)
	onHand, exists := v.inWarehouse[skupc]
	if !exists {
		onHand = v.quantities(i.WarehouseLocation)
		v.inWarehouse[skupc] = onHand
	}
	ratio := float64(i.Quantity) / float64(onHand)
	if ratio > 1 {
		ratio = math.Inf(1)
	}
	return ratio
}

// SKUPC returns the respective SKU or UPC depending on which the vendor uses.
func (v *Vars) skupc(i item) string {
	if !v.settings[v.toVendor(i.Name)].UseUPC {
		return i.SKU
	}
	return i.UPC
}

func (v *Vars) poNum(i *item, t time.Time) string {
	return util.S(v.settings[v.toVendor(i.Name)].PONum, "-", t.Format("20060102"))
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
func (o order) grade(v *Vars) float64 {
	ratios := 0.0
	for _, i := range o.Items {
		ratios += i.grade(v)
	}
	if ratios == math.Inf(1) {
		v.broken[o.OrderID] = true
	}
	return ratios / float64(len(o.Items))
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
func (v *Vars) quantities(s string) int {
	sum := 0
	houses := v.localOnly.FindAllString(s, -1)
	for _, house := range houses {
		quans := v.quantity.FindAllString(house, -1)
		for _, quan := range quans {
			val := un.Int(strconv.Atoi(v.number.FindString(quan)))
			sum += val
		}
	}
	return sum
}
