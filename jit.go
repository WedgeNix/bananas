package bananas

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/awsapi/types"
	"github.com/WedgeNix/util"
	"github.com/mrmiguu/print"
)

type jit struct {
	ac        *awsapi.Controller
	sc        *skuvault.Ctr
	monDir    dir.BananasMon
	cfgFile   file.BananasCfg
	utc       time.Time
	soldToday map[string]bool
	bans      bananas
}

// create a new just-in-time handler
func newJIT() (<-chan jit, <-chan error) {
	jc := make(chan jit)
	errc := make(chan error, 1)

	go func() {
		defer close(jc)
		defer close(errc)

		j := jit{utc: time.Now().UTC(), monDir: dir.BananasMon{}}

		ac, err := awsapi.New()
		if err != nil {
			errc <- err
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
			errc <- err
			rdc <- false
			return
		}
		if !exists {
			j.cfgFile.LastLA = util.LANow().Add(-24 * time.Hour)
		}

		errc <- j.ac.OpenDir(dir.BananasMonName, j.monDir)
		rdc <- true
	}()

	return rdc, errc
}

type newSKU string

// combs through the orders and updates entries in the AWS monitor directory
func (j *jit) updateAWS(rdc <-chan read, v *Vars, ords []order) (<-chan newSKU, <-chan error) {
	skuc := make(chan newSKU)
	errc := make(chan error, 1)

	go func() {
		defer v.rdOrdWg.Done()
		defer close(skuc)
		defer close(errc)

		// print.Msg("waiting on read channel")
		if !<-rdc {
			errc <- errors.New("unable to update in-memory monitor SKUs; not opened first")
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
					vendMon.AvgWait = 5
					vendMon.SKUs = types.SKUs{}
				}

				// if monitor SKU doesn't exist then throw its SKU into
				// the payload for populating later
				monSKU, exists := vendMon.SKUs[itm.SKU]
				if !exists {
					skuc <- newSKU(itm.SKU)
				}
				monSKU.Sold += itm.Quantity

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

func (j *jit) updateNewSKUs(skuc <-chan newSKU, v *Vars, ords []order) (<-chan updated, <-chan error) {
	upc := make(chan updated)
	errc := make(chan error, 1)

	go func() {
		defer close(upc)
		defer close(errc)

		pay := skuvault.GetProducts{
			PageSize: 10000,
		}

		print.Debug("absorbing new SKUs into AWS")

		// fill up non-repeating SKUs into payload's product SKU list
		for sku := range skuc {
			pay.ProductSKUs = append(pay.ProductSKUs, string(sku))
		}

		if len(pay.ProductSKUs) < 1 {

			print.Debug("No new SKUs to populate the monitor file")

			v.rdOrdWg.Done()
			fmt.Println("updateNewSKUs done")
			errc <- nil
			for {
				upc <- true
			}
		}

		print.Debug("grabbing SkuVault product information")

		// these are all brand new entries on the AWS database
		resp := j.sc.Products.GetProducts(&pay)
		for _, err := range resp.Errors {
			err, ok := err.(error)
			if err == nil {
				continue
			}
			if !ok {
				err = errors.New("SkuVault errors not converted correctly")
			}
			fmt.Println("updateNewSKUs done")
			v.rdOrdWg.Done()
			errc <- err
			upc <- false
			return
		}

		print.Debug("updating SKU creation dates")

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

func locsAndExterns(locs []skuvault.SkuLocations) (w1 int, w2 int) {
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
	panic("skuToMonVend: can't find sku in monitor directory")
}

func (j *jit) monToSKUs(poDay bool) []string {
	skus := []string{}

	for vend, mon := range j.monDir {
		for sku, monSKU := range mon.SKUs {
			daysOld := max(int(j.utc.Sub(monSKU.LastUTC).Hours()/24+0.5), 1)
			print.Msg(`daysOld=` + strconv.Itoa(daysOld))
			_, soldToday := j.soldToday[sku]
			expired := daysOld > monSKU.ProbationPeriod

			if soldToday && monSKU.Days > 0 {
				monSKU.Days += daysOld
			} else if soldToday {
				monSKU.Days = 1
				expired = false
			}
			monSKU.ProbationPeriod = min(8100/daysOld, 90)

			// save the monitor SKU after days was changed
			mon.SKUs[sku] = monSKU

			if expired {
				delete(mon.SKUs, sku)
				continue
			}
			if !poDay {
				continue
			}
			if monSKU.Pending {
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

	return skus
}

func (j *jit) prepareMonMail(updateCh <-chan updated, v *Vars) {

	print.Debug("matching P.O. days with today")

	poDay := false
	weekdayLA := util.LANow().Weekday()
	for _, day := range j.cfgFile.PODays {
		if weekdayLA != day {
			continue
		}
		poDay = true
	}

	print.Msg("prepareMonMail waiting on updated channel")
	<-updateCh

	bans := bananas{}

	skus := j.monToSKUs(poDay)
	if len(skus) < 1 {
		return
	}
	pay := skuvault.GetInventoryByLocation{ProductSKUs: skus}
	resp := j.sc.Inventory.GetInventoryByLocation(&pay)

	for sku, locs := range resp.Items {
		w1, w2 := locsAndExterns(locs)

		if w2 < 1 {
			continue
		}

		vend, avgWait, monSKU := j.vendAvgWaitMonSKU(sku)

		f := float64(monSKU.Sold) / float64(monSKU.Days)
		rp := (avgWait + float64(v.settings[vend].ReordPtAdd)) * f
		rtrdr := math.Min(float64(monSKU.Days)/float64(j.cfgFile.OrdXDaysWorth), 1)

		if float64(w1) > rp {
			continue
		}

		qt := int(float64(j.cfgFile.OrdXDaysWorth)*f*rtrdr + 0.5)
		min(qt, w2)

		bans[vend] = append(bans[vend], banana{sku, qt})
	}

	j.bans = bans
}

// orders monitor SKUs only via email
func (j *jit) order(v *Vars) []error {

	print.Debug("parse HTML template")

	tmpl, err := template.ParseFiles("vendor-email-tmpl.html")
	if err != nil {
		return []error{err}
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

	for vend, bun := range j.bans {
		vend := vend
		bun := bun
		emailing.Add(1)
		go func() {
			defer print.Debug("goroutine is finished emailing an email")
			defer emailing.Done()

			print.Debug("goroutine is starting to email a just-in-time vendor")

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
				print.Msg(vend, " ==> ", bun)
				mailerrc <- err
				return
			}

			to := []string{login.User}
			if !sandbox {
				to = append(v.settings[vend].Email, to...)
			}

			attachment := ""
			if v.settings[vend].FileDownload && len(bun) > 0 {
				attachment = bun.csv(vend)
			}

			if !paperless || !monitoring {
				email := buf.String()
				attempts := 0
				for {
					err := login.Email(to, "WedgeNix PO#: "+po, email, attachment)
					attempts++
					if err != nil {
						if attempts <= 3 {
							print.Msg("Failed to send email. [retrying]")
							t := time.Duration(3 * attempts)
							time.Sleep(t * time.Second)
							continue
						} else {
							print.Msg("Failed to send email! [FAILED]")
							print.Msg(vend, " ==> ", bun)
							delete(j.bans, vend) // remove so it doesn't get tagged; rerun
							mailerrc <- errors.New("failed to email " + vend)
							return
						}
					}
					return
				}
			}
		}()
	}

	print.Debug("wait for goroutines to finish emailing")
	emailing.Wait()
	print.Msg("Emailing round-trip: ", time.Since(start))

	// set all emailed bananas to pending
	for vend, mon := range j.monDir {
		for _, ban := range j.bans[vend] {
			monSKU := mon.SKUs[ban.SKUPC]
			monSKU.Pending = true
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

		if !monitoring {
			errc <- nil
			return
		}

		if !<-upc {
			errc <- errors.New("unable to save to AWS; SKUs not updated")
			return
		}

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

		err := j.ac.Save(file.BananasCfgName, j.cfgFile)
		if err != nil {
			errc <- err
			return
		}

		errc <- j.ac.SaveDir(dir.BananasMonName, j.monDir)
	}()

	return errc
}

// adjusts quantity in bananas for individual SKUs based on AWS data
// only handled on the specific PO dates
// func (j *jit) addMonsToBans(upc <-chan updated, v *Vars, b bananas) (<-chan bananas, <-chan error) {
// bansc := make(chan bananas)
// errc := make(chan error, 1)

// go func() {
// 	defer close(bansc)
// 	defer close(errc)

// print.Msg("waiting on updatedc")

// if !<-upc {
// 	errc <- errors.New("unable to add monitor orders to emails; SKUs not updated")
// 	return
// }

// print.Debug("matching P.O. days with today")

// poDay := false
// weekdayLA := util.LANow().Weekday()
// for _, day := range j.cfgFile.PODays {
// 	if weekdayLA != day {
// 		continue
// 	}
// 	poDay = true
// }

// print.Debug("adjusting to-order quantities of all SKUs in monitors")

// bans := b
// for vend, mon := range j.monDir {
// 	for sku, monSKU := range mon.SKUs {
// daysOld := max(int(j.utc.Sub(monSKU.LastUTC).Hours()/24+0.5), 1)
// _, soldToday := j.soldToday[sku]
// expired := daysOld > monSKU.ProbationPeriod

// if soldToday && monSKU.Days > 0 {
// 	monSKU.Days += daysOld
// } else if soldToday {
// 	monSKU.Days = 1
// }
// monSKU.ProbationPeriod = min(8100/daysOld, 90)
// // save the monitor SKU after days was changed
// mon.SKUs[sku] = monSKU

// f := float64(monSKU.Sold) / float64(monSKU.Days)
// rp := (mon.AvgWait + float64(v.settings[vend].ReordPtAdd)) * f
// rtrdr := math.Min(float64(monSKU.Days)/float64(j.cfgFile.OrdXDaysWorth), 1)

// bun := bans[vend]
// for i, ban := range bun {
// 	if ban.SKUPC != sku {
// 		continue
// 	}

// if expired {
// 	delete(mon.SKUs, sku)
// 	// bun = append(bun[:i], bun[i+1:]...)
// 	continue
// }

// if !poDay {
// 	// bun = append(bun[:i], bun[i+1:]...)
// 	continue
// }

// if float64(v.onHand[sku]) > rp {
// 	// bun = append(bun[:i], bun[i+1:]...)
// 	continue
// }

// if monSKU.Pending {
// 	// bun = append(bun[:i], bun[i+1:]...)
// 	continue
// }

// if monSKU.ProbationPeriod < 90 {
// 	// bun = append(bun[:i], bun[i+1:]...)
// 	continue
// }

//
// CONTINUE YESTERDAY'S WORK
//
// Pull W1 quantities from ItemLocation call from SkuVault
//
// Add in monitor skus to bananas that current don't exist
// but do so in the AWS file
//
// Remove monitor SKUs from bananas that need to be
//
// Update quantities of SKUs that are changed via calculations
// read in from the AWS monitor file already inside bananas
//

// ban.Quantity = int(float64(j.cfgFile.OrdXDaysWorth)*f*rtrdr + 0.5)

// save the banana in the bunch
// 	bun[i] = ban
// }

// save the bunch in the bananas
// 	bans[vend] = bun

// }

// save the vendor monitor after monitor SKU was changed
// 	j.monDir[vend] = mon
// }

// 	errc <- nil
// 	bansc <- bans
// }()

// return bansc, errc
// }

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
