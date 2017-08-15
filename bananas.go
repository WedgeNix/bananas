package bananas

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"regexp"

	"math"

	"strings"

	"time"

	"github.com/WedgeNix/util"
	"github.com/WedgeNix/warehouse-settings"

	"bytes"
	"html/template"
	"os"

	"io/ioutil"

	"encoding/json"

	"github.com/WedgeNix/warehouse-settings/app"
	"github.com/mrmiguu/un"
)

const (
	// removes real world side effects to testing purposes
	sandbox    = false
	paperless  = false
	ignoreCF1  = false
	monitoring = true

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
	// settings holds all static vendor warehouse information.
	settings app.Bananas

	vendExprs map[string]*regexp.Regexp

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

	original []order

	rdOrdWg sync.WaitGroup

	// holds onto errors that need to be handled later for damage control
	errs []error
}

func (v *Vars) err(err ...error) []error {
	v.errs = append(v.errs, err...)
	return v.errs
}

func (p *payload) preserveItems() []order {
	ords := make([]order, len(p.Orders))

	for i, ord := range p.Orders {
		itms := make([]item, len(ord.Items))
		copy(itms, ord.Items)
		ord.Items = itms

		ords[i] = ord
	}

	return ords
}

// Run initializes all package-level variables.
func Run() []error {
	if hit && !paperless {
		return []error{util.NewErr("bananas already hit; we don't want to re-email everyone")}
	}

	// pull environmental variables
	shipKey = os.Getenv("SHIP_API_KEY")
	shipSecret = os.Getenv("SHIP_API_SECRET")
	settingsURL = os.Getenv("SETTINGS_URL")
	comEmailUser = os.Getenv("COM_EMAIL_USER")
	comEmailPass = os.Getenv("COM_EMAIL_PASS")
	comEmailSMTP = os.Getenv("COM_EMAIL_SMTP")
	appUser = os.Getenv("APP_EMAIL_USER")
	appPass = os.Getenv("APP_EMAIL_PASS")

	util.Log("grab vendor settings for bananas")

	wc := wedgenix.New()
	sets := app.Bananas{}
	wc.Do(&sets)
	if len(sets) < 1 {
		return []error{util.NewErr("empty settings response")}
	}

	// printJSON(sets)

	// compile all vendor name expressions
	exprs := map[string]*regexp.Regexp{}
	for vend, set := range sets {
		exprs[vend] = regexp.MustCompile(set.Regex)
	}

	jc, errc := newJIT()
	if err := <-errc; err != nil {
		return []error{util.Err(err)}
	}

	util.Log("reading from AWS")

	j := <-jc
	rdc, errc := j.readAWS()
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
		broken:      map[int]bool{},
		vendors:     map[string]string{},
		taggables:   []order{},
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
	if err = <-errc; err != nil {
		return v.err(err)
	}

	util.Log("arrange the orders based on time-preference grading")

	arrangedPay, errs := v.arrangeOrders(filteredPay)
	v.err(errs...)

	util.Log("convert to stateful for in-order item quantities")

	bans, err := v.statefulConversion(arrangedPay)
	if err != nil {
		return v.err(err)
	}

	//
	// Honestly stateful stuff (real world)
	// ||||||||
	// vvvvvvvv

	util.Log("place higher needed quantities on top for emails")

	sortedBans := bans.sort().print()

	util.Log("email the respective orders")

	taggableBans, errs := v.order(sortedBans)
	v.err(errs...)

	util.Log("tag the orders on ShipStation")

	err = v.tagAndUpdate(taggableBans)
	v.err(err)

	util.Log("save config file on AWS")

	errc = v.j.saveAWSChanges(upc)
	if err = <-errc; err != nil {
		return v.err(err)
	}

	hit = true

	return v.errs
}

func printJSON(v interface{}) {
	util.Log(string(un.Bytes(json.MarshalIndent(v, "", "    "))))
}

// GetOrdersAwaitingShipment grabs an HTTP response of orders, filtering in those awaiting shipment.
func (v *Vars) getOrdersAwaitingShipment() (*payload, error) {
	pg := 1

	util.Log(`Bananas hit @`, util.LANow())

	pay := &payload{}
	reqs, secs, err := v.getPage(pg, pay)
	if err != nil {
		return pay, util.Err(err)
	}

	for pay.Page < pay.Pages {
		pg++
		ords := pay.Orders

		pay = &payload{}
		reqs, secs, err = v.getPage(pg, pay)
		if err != nil {
			return pay, util.Err(err)
		}
		if reqs < 1 {
			time.Sleep(time.Duration(secs) * time.Second)
		}

		pay.Orders = append(ords, pay.Orders...)
	}

	return pay, nil
}

// func fakeLAUTC(dotw int) (time.Time, time.Time) {
// 	dTime := time.Duration(dotw*24) * time.Hour
// 	util.Log(util.LANow().Add(dTime))
// 	return util.LANow().Add(dTime), time.Now().UTC().Add(dTime)
// }

// const theday = 0

func (v *Vars) getPage(page int, pay *payload) (int, int, error) {
	last := v.j.cfgFile.LastLA
	today := util.LANow()
	v.j.cfgFile.LastLA = today

	util.Log(`last=`, last)
	util.Log(`today=`, today)

	// 3/4ths of a day to give wiggle room for Matt's timing
	if today.Sub(last).Hours()/24 < 0.75 {
		return 0, 0, util.NewErr("same day still; reset AWS config LastLA date")
	}

	query := url.Values(map[string][]string{})
	query.Set(`page`, strconv.Itoa(page))
	query.Set(`createDateStart`, last.Format("2006-01-02 15:04:05"))
	query.Set(`createDateEnd`, today.Format("2006-01-02 15:04:05"))
	query.Set(`pageSize`, `500`)

	resp, err := v.login.Get(shipURL + `orders?` + query.Encode())
	if err != nil {
		return 0, 0, util.Err(err)
	}
	fmt.Println(shipURL + `orders?` + query.Encode())
	fmt.Println(resp.Status)

	err = json.NewDecoder(resp.Body).Decode(pay)
	if err != nil {
		return 0, 0, util.Err(err)
	}
	defer resp.Body.Close()
	// fmt.Println(*pay)

	remaining := resp.Header.Get("X-Rate-Limit-Remaining")
	reqs, err := strconv.Atoi(remaining)
	if err != nil {
		return 0, 0, util.Err(err)
	}
	reset := resp.Header.Get("X-Rate-Limit-Reset")
	secs, err := strconv.Atoi(reset)
	if err != nil {
		return reqs, 0, util.Err(err)
	}

	return reqs, secs, nil
}

// Payload is the first level of a ShipStation HTTP response body.
type payload struct {
	Orders []order
	Total  int
	Page   int
	Pages  int
}

func (v *Vars) isMonAndVend(i item) (bool, string) {
	for vend, set := range v.settings {
		if !set.Monitor || !monitoring {
			continue
		}
		if !v.vendExprs[vend].MatchString(i.Name) {
			continue
		}
		return true, vend
	}
	return false, ""
}

// FilterDropShipment scans all items of all orders looking for drop ship candidates.
func (v *Vars) filterDropShipment(pay *payload, rdc <-chan read) (filteredPayload, <-chan updated, <-chan error) {

	util.Log("copy payload and overwrite copy's orders")

	ords := pay.Orders

	if lO := len(pay.Orders); lO > 0 {
		util.Log("Pre-filter order count: ", lO)
	}
	pay.Orders = []order{}

	util.Log("go through all orders")

OrderLoop:
	for _, ord := range ords {
		// cf3 := ord.AdvancedOptions.CustomField3

		// if strings.Contains(cf3, "FAILED:") {

		// }

		// cf3 = strings.Replace(ord.AdvancedOptions.CustomField3, "FAILED:", "", -1)
		// 	for _, vend := range strings.Split(cf3, `;`) {
		// 	}

		if strings.ContainsAny(ord.AdvancedOptions.CustomField1, "Vv") && !(sandbox && ignoreCF1) {
			// util.Log("next order... [cf1] => ", ord.AdvancedOptions.CustomField1)
			continue OrderLoop
		}

		items := ord.Items
		ord.Items = []item{}
		for _, itm := range items {
			w2 := v.hasVendor.MatchString(itm.WarehouseLocation)
			mon, _ := v.isMonAndVend(itm)
			if !w2 && !mon {
				continue
			}
			ord.Items = append(ord.Items, itm)
		}

		if len(ord.Items) < 1 {
			continue
		}

		pay.Orders = append(pay.Orders, ord)
	}
	util.Log(`len(ords)=`, len(ords))
	v.rdOrdWg.Add(2)
	skuc, errca := v.j.updateAWS(rdc, v, ords)
	upc, errcb := v.j.updateNewSKUs(skuc, v, ords)
	v.j.prepareMonMail(upc, v)

	go func() {
		errs := v.j.order(v)
		for _, err := range errs {
			if err == nil {
				continue
			}
			util.Log(err)
		}
	}()

	v.rdOrdWg.Wait()
	util.Log(`len(pay.Orders)=`, len(pay.Orders))
	dsOrds := []order{}
	for _, ord := range pay.Orders {
		if ord.OrderStatus != "awaiting_shipment" {
			continue
		}
		dsOrds = append(dsOrds, ord)
	}

	if len(pay.Orders) < 1 {
		util.Log("No orders found after 'filtering'")
	}
	newFiltPay := filteredPayload(payload{Orders: dsOrds})
	return newFiltPay, upc, util.MergeErr(errca, errcb)
}

// Print prints the payload in a super minimal format.
func (v *Vars) print(p payload) error {
	for oi, o := range p.Orders {
		for ii, i := range o.Items {
			grade, err := o.grade(v)
			if err != nil {
				return err
			}
			skupc, err := v.skupc(i)
			if err != nil {
				return err
			}
			// fmt.Printf("%d/%d ~ %d/%d : %dx : %v | %s; %s\n", oi+1, cap(p.Orders), ii+1, cap(o.Items), i.Quantity, o.grade(v), v.skupc(i), i.WarehouseLocation)
			util.Log(
				oi+1, "/", cap(p.Orders),
				" ~ ",
				ii+1, "/", cap(o.Items),
				" : ",
				i.Quantity,
				" : ",
				grade,
				" | ",
				skupc, "; ",
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
func (v *Vars) statefulConversion(a arrangedPayload) (bananas, error) {
	bans := bananas{}
	innerBroke := []order{}

	util.Log("run over all orders/items")

	for _, o := range a.Orders {
		var savedO *order
		for _, i := range o.Items {
			skupc, err := v.skupc(i)
			if err != nil {
				return nil, util.Err(err)
			}
			if v.broken[o.OrderID] {
				v.add(bans, &i)
				savedO = &o
			} else if v.inWarehouse[skupc]-i.Quantity < 0 {
				innerBroke = append(innerBroke, o)
			} else {
				v.inWarehouse[skupc] -= i.Quantity
			}
		}
		if savedO != nil {
			v.taggables = append(v.taggables, *savedO)
		}
	}

	util.Log("hybrid orders that broke during stateful conversion")

	for _, o := range innerBroke {
		for _, i := range o.Items {
			v.add(bans, &i)
		}
		v.taggables = append(v.taggables, o)
	}

	return bans, nil
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
func (v *Vars) add(bans bananas, i *item) error {
	vend, err := v.toVendor(i.Name)
	if err != nil {
		return err
	}
	skupc, err := v.skupc(*i)
	if err != nil {
		return err
	}
	v.addBan(bans, vend, banana{skupc, i.Quantity})
	return nil
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
		util.Log(k, ":")
		for ik, iv := range v {
			util.Log("\t", ik, ": ", iv)
		}
	}
	util.Log()
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

	util.Log("create .csv for email attachment")

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

// Order sends email orders out to vendors for drop shipment product.
func (v *Vars) order(b bananas) (taggableBananas, []error) {

	util.Log("removing monitors from drop ship emails")

	for vend, set := range v.settings {
		if !set.Monitor {
			continue
		}
		delete(b, vend)
	}

	util.Log("parse HTML template")

	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		return nil, []error{util.Err(err)}
	}

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

	mailerrc := make(chan error)

	for V, set := range v.settings {
		if set.Monitor {
			continue
		}

		B, exists := b[V]
		if !exists {

			util.Log("send empty email")

		}
		vendor, bunch := V, B // new "variables" for closure

		emailing.Add(1)
		go func() {
			defer util.Log("goroutine is finished emailing an email")
			defer emailing.Done()

			util.Log("goroutine is starting to email a drop ship vendor")

			t := util.LANow()
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
				util.Log(vendor, " ==> ", bunch)
				mailerrc <- err
				return
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
				email := buf.String()
				attempts := 0
				for {
					err := login.Email(to, "WedgeNix PO#: "+po, email, attachment)
					attempts++
					if err != nil {
						if attempts <= 3 {
							util.Log("Failed to send email. [retrying]")
							t := time.Duration(3 * attempts)
							time.Sleep(t * time.Second)
							continue
						} else {
							util.Log("Failed to send email! [FAILED]")
							util.Log(vendor, " ==> ", bunch)
							delete(b, vendor) // remove so it doesn't get tagged; rerun
							mailerrc <- errors.New("failed to email " + vendor)
							return
						}
					}
					return
				}
			}
		}()
	}

	util.Log("wait for goroutines to finish emailing")
	emailing.Wait()
	util.Log("Emailing round-trip: ", time.Since(start))

	close(mailerrc)

	mailerrs := []error{}
	for mailerr := range mailerrc {
		if mailerr == nil {
			continue
		}
		mailerrs = append(mailerrs, mailerr)
	}

	if len(mailerrs) < 1 {
		mailerrs = nil
	}
	return taggableBananas(b), mailerrs
}

// TagAndUpdate tags all banana orders.
func (v *Vars) tagAndUpdate(b taggableBananas) error {
	util.Log("TAGGABLE_COUNT: ", len(v.taggables))

	for _, origOrd := range v.original {
		for i, tagOrd := range v.taggables {
			if origOrd.OrderNumber != tagOrd.OrderNumber {
				continue
			}

			var vSlice []string
			var upcSlice []string
			for _, item := range tagOrd.Items {
				poNum, err := v.poNum(&item, util.LANow())
				if err != nil {
					return err
				}
				vSlice = append(vSlice, poNum)
				upcSlice = append(upcSlice, item.UPC)
			}
			vList := onlyUnique(vSlice)
			upcList := onlyUnique(upcSlice)

			origOrd.AdvancedOptions.CustomField1 = vList
			origOrd.AdvancedOptions.CustomField2 = upcList
			v.taggables[i] = origOrd
		}
	}

	if len(v.taggables) > 0 && !sandbox {
		resp, err := v.login.Post(shipURL+`orders/createorders`, v.taggables)
		if err != nil {
			return err
		}
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		util.Log("CREATEORDERS_RESP: ", string(b))
	}

	if sandbox {
		// util.Log("NEW_TAGGABLES: ", v.taggables)
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
	skupc, err := v.skupc(i)
	if err != nil {
		return 0, util.Err(err)
	}
	onHand, exists := v.inWarehouse[skupc]
	if !exists {
		onHand, err = v.quantities(i.WarehouseLocation)
		if err != nil {
			return 0, util.Err(err)
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
	for vend, expr := range v.vendExprs {
		// if !strings.Contains(i.WarehouseLocation, set.Location) {
		// 	continue
		// }
		if !expr.MatchString(i.Name) {
			continue
		}
		return vend
	}
	return ""
}

// SKUPC returns the respective SKU or UPC depending on which the vendor uses.
func (v *Vars) skupc(i item) (string, error) {
	vend, err := v.toVendor(i.Name)
	if err != nil {
		return "", util.Err(err)
	}
	if !v.settings[vend].UseUPC {
		return i.SKU, nil
	}
	return i.UPC, nil
}

func (v *Vars) poNum(i *item, t time.Time) (string, error) {
	vend, err := v.toVendor(i.Name)
	return util.S(v.settings[vend].PONum, "-", t.Format("20060102")), util.Err(err)
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
			return 0, util.Err(err)
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
func (v *Vars) toVendor(itemName string) (string, error) {
	for vend := range v.settings {
		if !v.vendExprs[vend].MatchString(itemName) {
			continue
		}
		return vend, nil
	}
	return "", util.NewErr("vendor not found in '" + itemName + "'")
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
				return sum, util.Err(err)
			}
			sum += val
		}
	}
	return sum, nil
}
