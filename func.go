package bananas

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/WedgeNix/util"
	wedgemail "github.com/WedgeNix/wedgeMail"
	"github.com/mrmiguu/un"
)

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

func printJSON(v interface{}) {
	util.Log(string(un.Bytes(json.MarshalIndent(v, "", "    "))))
}

// func fakeLAUTC(dotw int) (time.Time, time.Time) {
// 	dTime := time.Duration(dotw*24) * time.Hour
// 	util.Log(util.LANow().Add(dTime))
// 	return util.LANow().Add(dTime), time.Now().UTC().Add(dTime)
// }

// const theday = 0

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
				println(util.Err(err).Error())
				continue
				// return nil, util.Err(err)
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
	if mon, vnd := v.isMonAndVend(*i); mon && !v.settings[vnd].Hybrid {
		println("[found non-hybrid monitor; not adding to bananas]")
		return nil
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
	for skupc, bunch := range b {
		sort.Slice(bunch, func(i, j int) bool {
			return bunch[i].Quantity > bunch[j].Quantity
		})
		b[skupc] = bunch
	}
	return b
}

// CSV writes a bunch of bananas into a comma-separated file, returning the buffer.
func (b bunch) csv(name string) (wedgemail.Attachment, error) {

	util.Log("create .csv for email attachment")

	att := wedgemail.Attachment{Name: name}
	buf := new(bytes.Buffer)
	csv := csv.NewWriter(buf)

	if err := csv.Write([]string{"SKU/UPC", "Quantity"}); err != nil {
		return att, err
	}
	for _, banana := range b {
		if err := csv.Write([]string{banana.SKUPC, itoa(banana.Quantity)}); err != nil {
			return att, err
		}
	}

	csv.Flush()
	att.Reader = buf
	return att, nil
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
