package bananas

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"regexp"
	"sync"
	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/OuttaLineNomad/skuvault/inventory"
	"github.com/OuttaLineNomad/skuvault/products"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/awsapi/types"
	"github.com/WedgeNix/util"
	wedgemail "github.com/WedgeNix/wedgeMail"
)

const (
	emailUsJITOnly = false
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
	sku2upc   map[string]string
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
			sku2upc: make(map[string]string),
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
func (j *jit) readAWS() (<-chan read, <-chan error) {
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

type newSKUPC string

// combs through the orders and updates entries in the AWS monitor directory
func (j *jit) updateAWS(rdc <-chan read, v *Vars, ords []order) (<-chan newSKUPC, <-chan error) {
	skupcc := make(chan newSKUPC)
	errc := make(chan error, 1)

	go func() {
		defer v.rdOrdWg.Done()
		defer close(skupcc)
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

				// if isUPC(itm.SKU) {
				// 	msg := "SKU '" + itm.SKU + "' is identified as a UPC"
				// 	errc <- util.NewErr(msg)
				// 	fmt.Println(msg)
				// 	return
				// }

				if v.settings[vend].UseUPC {
					j.sku2upc[itm.SKU] = itm.UPC
				}

				// determine existence of vendor monitor file
				vendMon, exists := j.monDir[vend]
				if !exists {
					vendMon.AvgWait = 5
					vendMon.SKUs = types.SKUs{}
				}

				skupc := itm.SKU
				// if v.settings[vend].UseUPC {
				// 	skupc = itm.UPC
				// }

				// if monitor SKU doesn't exist then throw its SKU into
				// the payload for populating later
				monSKU, exists := vendMon.SKUs[skupc]
				if !exists {
					util.Log(`'` + skupc + `' found!`)
					skupcc <- newSKUPC(skupc)
				}
				monSKU.Sold += itm.Quantity

				j.soldToday[skupc] = true

				// overwrite the changes on both a file level and directory level
				vendMon.SKUs[skupc] = monSKU
				j.monDir[vend] = vendMon
			}
		}

		fmt.Println("updateAWS done")
		errc <- nil
	}()

	return skupcc, errc
}

type updated bool

func (j *jit) updateNewSKUPCs(skupcc <-chan newSKUPC, v *Vars, ords []order) (<-chan updated, <-chan error) {
	updc := make(chan updated)
	errc := make(chan error, 1)

	go func() {
		defer close(updc)
		defer close(errc)

		pay := products.GetProducts{
			PageSize: 10000,
		}

		util.Log("absorbing new SKUs into AWS")

		// fill up non-repeating SKUs into payload's product SKU list
		for newskupc := range skupcc {
			skupc := string(newskupc)
			pay.ProductSKUs = append(pay.ProductSKUs, skupc)
			pay.ProductCodes = append(pay.ProductCodes, skupc)
		}

		if len(pay.ProductSKUs) < 1 && len(pay.ProductCodes) < 1 {

			util.Log("No new SKUs to populate the monitor file")

			v.rdOrdWg.Done()
			fmt.Println("updateNewSKUs done")
			errc <- nil
			for {
				updc <- true
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
			updc <- false
			return
		}

		util.Log("updating SKU creation dates")

		// update all monitor SKUs from response products
		for _, prod := range resp.Products {
			for _, ord := range ords {
				for _, itm := range ord.Items {
					if len(itm.SKU) > 0 && len(prod.Sku) > 0 && itm.SKU != prod.Sku { //|| // SKU
						//len(itm.UPC) > 0 && len(prod.Code) > 0 && itm.UPC != prod.Code { // UPC
						continue
					}

					skupc, err := v.skupc(itm)
					if err != nil {
						errc <- util.Err(err)
						updc <- false
						return
					}
					monDir := j.monDir[v.getVend(itm)]
					monSKUPC := monDir.SKUs[skupc]
					// daysOld := int(j.utc.Sub(prod.CreatedDateUtc).Hours()/24 + 0.5)
					// monSKUPC.ProbationPeriod = min(8100/daysOld, 90)
					monSKUPC.LastUTC = prod.CreatedDateUtc
					monDir.SKUs[skupc] = monSKUPC

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
			updc <- true
		}
	}()

	return updc, errc
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

func (j *jit) vendAvgWaitMonSKUPC(skupc string) (string, float64, types.BananasMonSKU, error) {
	for vend, mon := range j.monDir {
		for mskupc, monSKUPC := range mon.SKUs {
			if skupc != mskupc {
				continue
			}
			return vend, mon.AvgWait, monSKUPC, nil
		}
	}
	return "", 0, types.BananasMonSKU{}, errors.New("skuToMonVend: can't find sku in monitor directory")
}

func (j *jit) monToSKUPCs(poDay bool) ([]string, error) {
	skupcs := []string{}

	for vend, mon := range j.monDir {
		for skupc, monSKUPC := range mon.SKUs {
			daysOld := max(int(j.utc.Sub(monSKUPC.LastUTC).Hours()/24+0.5), 1)
			// util.Err(`daysOld=` + itoa(daysOld))
			_, soldToday := j.soldToday[skupc]
			expired := daysOld > monSKUPC.ProbationPeriod

			if soldToday && monSKUPC.Days > 0 {
				monSKUPC.Days += daysOld
				monSKUPC.LastUTC = j.utc
			} else if soldToday {
				monSKUPC.Days = 1
				monSKUPC.LastUTC = j.utc
				expired = false
			}
			if soldToday {
				monSKUPC.ProbationPeriod = min(8100/daysOld, 90)
			}

			// save the monitor SKU after days was changed
			mon.SKUs[skupc] = monSKUPC

			if expired {
				delete(mon.SKUs, skupc)
				continue
			}
			if !poDay {
				continue
			}
			if !monSKUPC.Pending.IsZero() {
				continue
			}
			if monSKUPC.ProbationPeriod < 90 {
				continue
			}

			skupcs = append(skupcs, skupc)
		}

		// save monitor file
		j.monDir[vend] = mon
	}

	if len(skupcs) > 0 && !poDay {
		return nil, errors.New("trying to add skus to monitor email on non-po day")
	}

	return skupcs, nil
}

// isUPC could be used in the future to determine whether something
// is a UPC or not.
func isUPC(s string) bool {
	return regexp.MustCompile(`[0-9]{12,13}`).MatchString(s)
}

func (j *jit) prepareMonMail(updateCh <-chan updated, v *Vars) error {

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

	skupcs, err := j.monToSKUPCs(poDay)
	if err != nil {
		return err
	}
	if len(skupcs) < 1 {
		return nil
	}

	// var skus, upcs []string
	// for _, skupc := range skupcs {
	// 	if isUPC(skupc) {
	// 		upcs = append(upcs, skupc)
	// 	} else {
	// 		skus = append(skus, skupc)
	// 	}
	// }
	skuresp := j.sc.Inventory.GetInventoryByLocation(&inventory.GetInventoryByLocation{
		ProductSKUs: toSet(skupcs),
		// ProductCodes: upcs,
	})
	for _, err := range skuresp.Errors {
		return fmt.Errorf("GetInventoryByLocation: %s", err)
	}

	for skupc, locs := range skuresp.Items {
		w1, w2 := locsAndExterns(locs)

		if w2 < 1 {
			continue
		}

		vend, avgWait, monSKUPC, err := j.vendAvgWaitMonSKUPC(skupc)
		if err != nil {
			return err
		}

		daysOld := int(j.utc.Sub(monSKUPC.LastUTC).Hours()/24 + 0.5)
		monSKUPC.Days += daysOld

		f := float64(monSKUPC.Sold) / float64(monSKUPC.Days)
		rp := (avgWait + float64(v.settings[vend].ReordPtAdd)) * f
		rtrdr := math.Min(float64(monSKUPC.Days)/float64(j.cfgFile.OrdXDaysWorth), 1)

		if w1 > 0 && rtrdr < 1 && monSKUPC.Sold == 1 {
			continue
		}

		if w1 > int(rp) {
			continue
		}

		qt := int(float64(j.cfgFile.OrdXDaysWorth)*f*rtrdr + 0.5)
		min(qt, w2)

		j.bans[vend] = append(j.bans[vend], banana{skupc, qt})
	}

	return nil
}

func toSet(skus []string) []string {
	m := make(map[string]interface{})
	for _, sku := range skus {
		m[sku] = nil
	}
	var s []string
	for sku := range m {
		s = append(s, sku)
	}
	return s
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

// orders monitor SKUs only via email
func (j *jit) order(v *Vars) []error {

	util.Log("cleaning monitor bananas")
	j.bans.clean()

	util.Log("sorting monitor bananas")
	j.bans.sort()

	util.Log("parse HTML template")

	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		return []error{util.Err(err)}
	}

	login, err := wedgemail.StartMail()
	if err != nil {
		return []error{util.Err(err)}
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

	mailerrc := make(chan error)

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
			mailerrc <- util.Err(err)
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
				mailerrc <- err
			}
		}

		if !paperless {
			email := buf.String()
			err := login.Email(to, "WedgeNix PO#: "+po, email, att)
			if err != nil {
				mailerrc <- errors.New("error in emailing " + vend)
			}
		}
	}

	util.Log("Joining hybrids with Monitor-emailing (no doubles)")
	for vend, bun := range <-j.hybrids {
		sum := map[string]int{}
		for _, ban := range bun { // drop ship bananas
			sum[ban.SKUPC] += ban.Quantity
		}
		useUPC := v.settings[vend].UseUPC
		for _, ban := range j.bans[vend] { // monitor-only bananas
			skupc := ban.SKUPC
			if useUPC && !isUPC(skupc) {
				skupc = j.sku2upc[skupc]
			}
			sum[skupc] += ban.Quantity
		}
		j.bans[vend] = nil
		for skupc, qt := range sum {
			j.bans[vend] = append(j.bans[vend], banana{skupc, qt})
		}
	}

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
			monSKU := mon.SKUs[ban.SKUPC]
			monSKU.Pending = util.LANow()
			mon.SKUs[ban.SKUPC] = monSKU
		}
		j.monDir[vend] = mon
	}

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
	return mailerrs
}

// record all or no changes made to the monitor directory asynchronously
func (j *jit) saveAWSChanges(upc <-chan updated) <-chan error {
	errc := make(chan error)

	go func() {
		defer close(errc)

		// err := j.ac.SaveFile("hit-the-bananas/logs/", util.GetLogFile())
		// if err != nil {
		// 	errc <- util.Err(err)
		// }

		if !monitoring {
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
		for skupc := range j.soldToday {
			for vend, mon := range j.monDir {
				monSKUPC, exists := mon.SKUs[skupc]
				if !exists {
					continue
				}
				monSKUPC.LastUTC = j.utc
				mon.SKUs[skupc] = monSKUPC
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
