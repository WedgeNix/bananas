package bananas

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"sync"
	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/OuttaLineNomad/skuvault/inventory"
	"github.com/OuttaLineNomad/skuvault/products"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/awsapi/types"
	"github.com/WedgeNix/bananas/ship"
	"github.com/WedgeNix/bananas/skc"
	"github.com/WedgeNix/bananas/whs"
	"github.com/WedgeNix/util"
	wedgemail "github.com/WedgeNix/wedgeMail"
)

type jit struct {
	ac        *awsapi.Controller
	sc        *skuvault.Ctr
	monDir    dir.BananasMon
	cfgFile   file.BananasCfg
	utc       time.Time
	soldToday map[string]bool
	bans      bananas
	hybrids   chan bananas
	mailErr   chan error
}

// create a new just-in-time handler
func newJIT() (<-chan jit, <-chan error) {
	jc := make(chan jit)
	errc := make(chan error, 1)

	go func() {
		defer close(jc)
		defer close(errc)

		j := jit{
			utc:     time.Now().UTC(),
			monDir:  dir.BananasMon{},
			bans:    bananas{},
			hybrids: make(chan bananas, 1),
			mailErr: make(chan error, 1),
		}

		ac, err := awsapi.New()
		if err != nil {
			errc <- util.Err(err)
			jc <- j
			return
		}

		j.ac = ac
		j.sc = skuvault.NewEnvCredSession()
		j.soldToday = make(map[string]bool)

		errc <- nil
		jc <- j
	}()

	return jc, errc
}

type read bool

// read in the entire monitor directory asynchronously
func (j *jit) ReadAWS() (<-chan read, <-chan error) {
	rdc := make(chan read)
	errc := make(chan error, 1)

	go func() {
		defer close(rdc)
		defer close(errc)

		exists, err := j.ac.Open(file.BananasCfgName, &j.cfgFile)
		if err != nil {
			errc <- util.Err(err)
			rdc <- false
			return
		}
		if !exists {
			j.cfgFile.LastLA = util.LANow().Add(-24 * time.Hour)
		}

		err = j.ac.OpenDir(dir.BananasMonName, j.monDir)
		if err != nil {
			errc <- util.Err(err)
			rdc <- false
			return
		}
		errc <- nil
		rdc <- true
	}()

	return rdc, errc
}

type newSKU string

// combs through the orders and updates entries in the AWS monitor directory
func (j *jit) UpdateAWS(rdc <-chan read, v *Vars, ords []ship.Order) (<-chan newSKU, <-chan error) {
	skuc := make(chan newSKU)
	errc := make(chan error, 1)

	go func() {
		defer v.rdOrdWg.Done()
		defer close(skuc)
		defer close(errc)

		// util.Err("waiting on read channel")
		if !<-rdc {
			errc <- util.NewErr("unable to update in-memory monitor SKUs; not opened first")
			fmt.Println("updateAWS done")
			return
		}

		// go through and see if any entries need to be pulled from SkuVault
		// also increase sold count once for each time the SKU is found
		for _, ord := range ords {
			for _, itm := range ord.Items {
				isMon, vend := v.isMonAndVend(itm)
				if !isMon {
					continue
				}

				// determine existence of vendor monitor file
				vendMon, exists := j.monDir[vend]
				if !exists {
					util.Log(`new monitor vendor '` + vend + `' discovered!`)
					vendMon.AvgWait = 5
					vendMon.SKUs = types.SKUs{}
				}

				// if monitor SKU doesn't exist then throw its SKU into
				// the payload for populating later
				monSKU, exists := vendMon.SKUs[itm.SKU]
				if !exists {
					util.Log(`new monitor sku '` + itm.SKU + `' discovered!`)
					skuc <- newSKU(itm.SKU)
				}
				monSKU.Sold += itm.Quantity
				monSKU.UPC = itm.UPC

				j.soldToday[itm.SKU] = true

				// overwrite the changes on both a file level and directory level
				vendMon.SKUs[itm.SKU] = monSKU
				j.monDir[vend] = vendMon
			}
		}

		fmt.Println("updateAWS done")
		errc <- nil
	}()

	return skuc, errc
}

type updated bool

func (j *jit) UpdateNewSKUs(skuc <-chan newSKU, v *Vars, ords []ship.Order) (<-chan updated, <-chan error) {
	upc := make(chan updated)
	errc := make(chan error, 1)

	go func() {
		defer close(upc)
		defer close(errc)

		pay := products.GetProducts{
			PageSize: 10000,
		}

		util.Log("absorbing new SKUs into AWS")

		// fill up non-repeating SKUs into payload's product SKU list
		for sku := range skuc {
			pay.ProductSKUs = append(pay.ProductSKUs, string(sku))
		}

		if len(pay.ProductSKUs) < 1 {

			util.Log("No new SKUs to populate the monitor file")

			v.rdOrdWg.Done()
			fmt.Println("updateNewSKUs done")
			errc <- nil
			for {
				upc <- true
			}
		}

		util.Log("grabbing SkuVault product information")

		// these are all brand new entries on the AWS database
		resp := j.sc.Products.GetProducts(&pay)
		for _, err := range resp.Errors {
			err, ok := err.(error)
			if err == nil {
				continue
			}
			if !ok {
				err = errors.New("skuVault errors not converted correctly")
			}
			fmt.Println("updateNewSKUs done")
			v.rdOrdWg.Done()
			errc <- util.Err(err)
			upc <- false
			return
		}

		util.Log("updating SKU creation dates")

		// update all monitor SKUs from response products
		for _, prod := range resp.Products {
			for _, ord := range ords {
				for _, itm := range ord.Items {
					if itm.SKU != prod.Sku {
						continue
					}

					monDir := j.monDir[v.getVend(itm)]
					monSKU := monDir.SKUs[prod.Sku]
					// daysOld := int(j.utc.Sub(prod.CreatedDateUtc).Hours()/24 + 0.5)
					// monSKU.ProbationPeriod = min(8100/daysOld, 90)
					monSKU.LastUTC = prod.CreatedDateUtc
					monDir.SKUs[prod.Sku] = monSKU

					// actually overwrite the empty monitor SKU
					j.monDir[v.getVend(itm)] = monDir
					break
				}
			}
		}

		fmt.Println("updateNewSKUs done")
		v.rdOrdWg.Done()
		errc <- nil
		for {
			upc <- true
		}
	}()

	return upc, errc
}

func locsAndExterns(locs []inventory.SkuLocations) (w1 int, w2 int) {
	for _, loc := range locs {
		if loc.WarehouseCode == "W1" {
			w1 = loc.Quantity
		} else {
			w2 += loc.Quantity
		}
	}
	return
}

func (j *jit) vendAvgWaitMonSKU(sku string) (string, float64, types.BananasMonSKU) {
	for vend, mon := range j.monDir {
		for msku, monSKU := range mon.SKUs {
			if sku != msku {
				continue
			}
			return vend, mon.AvgWait, monSKU
		}
	}
	panic("skuToMonVend: can't find sku:" + sku + " in monitor directory")
}

func (j *jit) monToSKUs(poDay bool) ([]string, error) {
	skus := []string{}

	for vend, mon := range j.monDir {
		for sku, monSKU := range mon.SKUs {
			daysOld := max(int(j.utc.Sub(monSKU.LastUTC).Hours()/24+0.5), 1)
			// util.Err(`daysOld=` + itoa(daysOld))
			_, soldToday := j.soldToday[sku]
			expired := daysOld > monSKU.ProbationPeriod

			if soldToday && monSKU.Days > 0 {
				monSKU.Days += daysOld
				monSKU.LastUTC = j.utc
			} else if soldToday {
				monSKU.Days = 1
				monSKU.LastUTC = j.utc
				expired = false
			}
			if soldToday {
				monSKU.ProbationPeriod = min(8100/daysOld, 90)
			}

			// save the monitor SKU after days was changed
			mon.SKUs[sku] = monSKU

			if expired {
				delete(mon.SKUs, sku)
				continue
			}
			if !poDay {
				continue
			}
			if !monSKU.Pending.IsZero() {
				continue
			}
			if monSKU.ProbationPeriod < 90 {
				continue
			}

			skus = append(skus, sku)
		}

		// save monitor file
		j.monDir[vend] = mon
	}

	if len(skus) > 0 && !poDay {
		return nil, errors.New("trying to add skus to monitor email on non-po day")
	}

	return skus, nil
}

func (j *jit) PrepareMonMail(updateCh <-chan updated, v *Vars) error {

	util.Log("matching P.O. days with today")

	poDay := false
	weekdayLA := util.LANow().Weekday()
	for _, day := range j.cfgFile.PODays {
		if weekdayLA != day {
			continue
		}
		poDay = true
	}

	util.Log("prepareMonMail waiting on updated channel")
	<-updateCh

	skus, err := j.monToSKUs(poDay)
	if err != nil {
		return err
	}
	if len(skus) < 1 {
		return nil
	}
	pay := inventory.GetInventoryByLocation{ProductSKUs: skus}
	resp := j.sc.Inventory.GetInventoryByLocation(&pay)

	for sku, locs := range resp.Items {
		w1, w2 := locsAndExterns(locs)

		if w2 == 0 {
			continue
		}

		vend, avgWait, monSKU := j.vendAvgWaitMonSKU(sku)

		daysOld := int(j.utc.Sub(monSKU.LastUTC).Hours()/24 + 0.5)
		monSKU.Days += daysOld

		f := float64(monSKU.Sold) / float64(monSKU.Days)
		rp := (avgWait + float64(v.settings[vend].ReordPtAdd)) * f
		rtrdr := math.Min(float64(monSKU.Days)/float64(j.cfgFile.OrdXDaysWorth), 1)

		if w1 > 0 && rtrdr < 1 && monSKU.Sold == 1 {
			continue
		}

		if w1 > int(rp) {
			continue
		}

		qt := int(float64(j.cfgFile.OrdXDaysWorth)*f*rtrdr + 0.5)
		// min(qt, w2)

		j.bans[vend] = append(j.bans[vend], banana{skc.SKUPC{SKU: sku}, sku, qt})

	}

	return nil
}

func (b bananas) clean() {
	for skupc, oldBunch := range b {
		var cleanBunch bunch
		for _, oldBanana := range oldBunch {
			if oldBanana.Quantity == 0 {
				continue
			}
			cleanBunch = append(cleanBunch, oldBanana)
		}
		b[skupc] = cleanBunch
	}
}

// emailOrders monitor SKUs only via email
func (j *jit) EmailOrders(v *Vars) {
	defer close(j.mailErr)

	util.Log("cleaning monitor bananas")
	j.bans.clean()

	util.Log("sorting monitor bananas")
	j.bans.sort()

	util.Log("filling monitor bananas with UPCs")
	if err := j.fillUPCs(j.bans); err != nil {
		j.mailErr <- err
		return
	}

	util.Log("parse HTML template")

	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		j.mailErr <- err
		return
	}

	login, err := wedgemail.StartMail()
	if err != nil {
		j.mailErr <- err
		return
	}

	// login := util.EmailLogin{
	// 	User: comEmailUser,
	// 	Pass: comEmailPass,
	// 	SMTP: comEmailSMTP,
	// }

	// if sandbox {
	// 	login.User = appUser
	// 	login.Pass = appPass
	// }

	var emailing sync.WaitGroup
	start := time.Now()

	mailErrc := make(chan error)

	emailThem := func(vend string, bun bunch) {
		defer util.Log("goroutine is finished emailing an email")
		defer emailing.Done()

		set := v.settings[vend]
		if set.Monitor && !set.Hybrid && len(bun) == 0 {
			util.Log("monitor-only; not emailing blank")
			return
		} else if len(bun) == 0 {
			util.Log("send empty email (hybrid)")
		}

		x := "Monitor"
		if set.Hybrid {
			x = "Hybrid"
		}
		util.Log("goroutine is starting to email a " + x + " vendor")

		t := util.LANow()
		po := v.settings[vend].PONum + "-" + t.Format("20060102")

		inj := injection{
			Vendor: vend,
			Date:   t.Format("01/02/2006"),
			PO:     po,
			Bunch:  bun,
		}

		buf := &bytes.Buffer{}
		err := tmpl.Execute(buf, inj)
		if err != nil {
			util.Log(vend, " ==> ", bun)
			mailErrc <- util.Err(err)
			return
		}

		to := []string{appUser}
		if !sandbox && !emailUsJITOnly {
			to = append(v.settings[vend].Email, to...)
		}

		var att wedgemail.Attachment
		if v.settings[vend].FileDownload && len(bun) > 0 {
			att, err = bun.csv(vend + ".csv")
			if err != nil {
				mailErrc <- err
			}
		}

		if sandbox {
			return
		}

		if !paperless {
			email := buf.String()
			err := login.Email(to, "WedgeNix PO#: "+po, email, att)
			if err != nil {
				mailErrc <- errors.New("error in emailing " + vend)
			}
		}
	}

	util.Log("Joining hybrids with Monitor-emailing (no doubles)")
	for vend, hyBun := range <-j.hybrids {
		sum := make(whs.Warehouse)
		for _, ban := range hyBun { // hybrid
			sum[ban.SKUPC] += ban.Quantity
		}
		for _, ban := range j.bans[vend] { // mon-only
			sum[ban.SKUPC] += ban.Quantity
		}
		j.bans[vend] = nil
		for skupc, qt := range sum {
			sets := v.settings[vend]
			prodID := skupc.SKU
			if sets.UseUPC {
				prodID = skupc.UPC
			}
			j.bans[vend] = append(j.bans[vend], banana{skupc, prodID, qt})
		}
	}

	util.Log("HYBRID/MONITOR:")
	j.bans = j.bans.sort().print()

	util.Log("Emailing any hybrids and/or monitors")
	for vend, bun := range j.bans {
		emailing.Add(1)
		go emailThem(vend, bun)
	}

	util.Log("Monitor: wait for goroutines to finish emailing")
	emailing.Wait()
	util.Log("Monitor: Emailing round-trip: ", time.Since(start))

	// set all emailed bananas to pending
	for vend, mon := range j.monDir {
		for _, ban := range j.bans[vend] {
			if ban.Quantity == 0 { // hybrids toss empties in, too; don't pen in
				continue
			}
			monSKU := mon.SKUs[ban.SKU]
			monSKU.Pending = util.LANow()
			mon.SKUs[ban.SKU] = monSKU
		}
		j.monDir[vend] = mon
	}

	close(mailErrc)

	for err := range mailErrc {
		j.mailErr <- err
	}
}

func (j *jit) fillUPCs(bans bananas) error {
	for vend, bun := range bans {
		for i, ban := range bun {
			monVend, found := j.monDir[vend]
			if !found {
				return errors.New("vendor '" + vend + "' not found")
			}
			monSKU, found := monVend.SKUs[ban.SKU]
			if !found {
				return errors.New("sku '" + ban.SKU + "' not found for '" + vend + "'")
			}
			if len(monSKU.UPC) == 0 {
				err := errors.New("no upc found for sku '" + ban.SKU + "'")
				// return
				log(err)
			}

			ban.UPC = monSKU.UPC
			bun[i] = ban
		}

		bans[vend] = bun
	}
	return nil
}

// record all or no changes made to the monitor directory asynchronously
func (j *jit) SaveAWSChanges(upc <-chan updated) <-chan error {
	errc := make(chan error)

	go func() {
		defer close(errc)

		// err := j.ac.SaveFile("hit-the-bananas/logs/", util.GetLogFile())
		// if err != nil {
		// 	errc <- util.Err(err)
		// }

		if dsOnly {
			errc <- nil
			return
		}

		if !<-upc {
			errc <- util.NewErr("unable to save to AWS; SKUs not updated")
			return
		}

		// MIGHT BE REDUNDANT
		// MIGHT BE REDUNDANT
		// MIGHT BE REDUNDANT
		for sku := range j.soldToday {
			for vend, mon := range j.monDir {
				monSKU, exists := mon.SKUs[sku]
				if !exists {
					continue
				}
				monSKU.LastUTC = j.utc
				mon.SKUs[sku] = monSKU
				j.monDir[vend] = mon
				break
			}
		}

		j.cfgFile.LastLA = util.LANow()

		err := j.ac.Save(file.BananasCfgName, j.cfgFile)
		if err != nil {
			errc <- util.Err(err)
			return
		}

		errc <- j.ac.SaveDir(dir.BananasMonName, j.monDir)
	}()

	return errc
}

func max(i, j int) int {
	if i > j {
		return i
	}
	return j
}

func min(i, j int) int {
	if i < j {
		return i
	}
	return j
}
