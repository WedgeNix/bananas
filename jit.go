package bananas

import (
	"errors"
	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/ext"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/util"
)

type jit struct {
	ac          *awsapi.Controller
	sc          *skuvault.Ctr
	monDir      dir.BananasMon
	cfgFile     file.BananasCfg
	utc         time.Time
	updateLater map[string]bool
}

// create a new just-in-time handler
func newJIT() (<-chan jit, <-chan error) {
	jc := make(chan jit)
	errc := make(chan error, 1)

	go func() {
		defer close(jc)
		defer close(errc)

		j := jit{utc: time.Now().UTC()}

		ac, err := awsapi.New(sandbox)
		if err != nil {
			errc <- err
			jc <- j
			return
		}

		j.ac = ac
		j.sc = skuvault.NewEnvCredSession()
		j.updateLater = make(map[string]bool)

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

		_, err := j.ac.Open(awsapi.BananasCfgFile, &j.cfgFile)
		if err != nil {
			errc <- err
			rdc <- false
			return
		}

		j.cfgFile.LastUTC = time.Now().UTC()

		errc <- j.ac.OpenDir(awsapi.BananasMonDir, j.monDir)
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
		defer close(skuc)
		defer close(errc)

		if !<-rdc {
			errc <- errors.New("unable to update in-memory monitor SKUs; not opened first")
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

				vendFile := vend + ext.BananasMon

				// determine existence of vendor monitor file
				vendMon, exists := j.monDir[vendFile]
				if !exists {
					j.monDir[vendFile] = file.BananasMon{}
				}

				// if monitor SKU doesn't exist then throw its SKU into
				// the payload for populating later
				monSKU, exists := vendMon.SKUs[itm.SKU]
				if !exists {
					skuc <- newSKU(itm.SKU)
				}
				monSKU.Sold += itm.Quantity

				j.updateLater[itm.SKU] = true

				// overwrite the changes on both a file level and directory level
				vendMon.SKUs[itm.SKU] = monSKU
				j.monDir[vendFile] = vendMon
			}
		}

		// for vend, mon := range j.monDir {
		// 	for sku, monSKU := range mon.SKUs {
		// 		monSKU.LastUTC = j.utc

		// 		// save changes on monitor SKU level
		// 		mon.SKUs[sku] = monSKU
		// 	}

		// 	// save changes on monitor vendor level
		// 	j.monDir[vend] = mon
		// }
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

		// fill up non-repeating SKUs into payload's product SKU list
		for sku := range skuc {
			pay.ProductSKUs = append(pay.ProductSKUs, string(sku))
		}

		// these are all brand new entries on the AWS database
		resp := j.sc.Products.GetProducts(&pay)
		for _, err := range resp.Errors {
			err := err.(error)
			if err == nil {
				continue
			}
			errc <- err
			upc <- false
			return
		}

		// update all monitor SKUs from response products
		for _, prod := range resp.Products {
			for _, ord := range ords {
				for _, itm := range ord.Items {
					if itm.SKU != prod.Sku {
						continue
					}

					monSKU := j.monDir[v.getVend(itm)].SKUs[prod.Sku]
					monSKU.LastUTC = prod.CreatedDateUtc

					// actually overwrite the empty monitor SKU
					j.monDir[v.getVend(itm)].SKUs[prod.Sku] = monSKU
				}
			}
		}

		errc <- nil
		for {
			upc <- true
		}
	}()

	return upc, errc
}

// record all or no changes made to the monitor directory asynchronously
func (j *jit) saveAWSChanges(upc <-chan updated) <-chan error {
	errc := make(chan error)

	go func() {
		defer close(errc)

		if !<-upc {
			errc <- errors.New("unable to save to AWS; SKUs not updated")
			return
		}

		for sku := range j.updateLater {
			for vend, mon := range j.monDir {
				monSKU := mon.SKUs[sku]
				monSKU.LastUTC = j.utc
				mon.SKUs[sku] = monSKU
				j.monDir[vend] = mon
			}
		}

		err := j.ac.Save(awsapi.BananasCfgFile, j.cfgFile)
		if err != nil {
			errc <- err
			return
		}

		errc <- j.ac.SaveDir(j.monDir)
	}()

	return errc
}

// adjusts quantity in bananas for individual SKUs based on AWS data
// only handled on the specific PO dates
func (j *jit) addMonsToBans(upc <-chan updated, v *Vars, b bananas) (<-chan bananas, <-chan error) {
	bansc := make(chan bananas)
	errc := make(chan error, 1)

	go func() {
		defer close(bansc)
		defer close(errc)

		if !<-upc {
			errc <- errors.New("unable to add monitor orders to emails; SKUs not updated")
			return
		}

		monDay := false
		weekdayLA := util.LANow().Weekday()
		for _, day := range j.cfgFile.PODays {
			if weekdayLA != day {
				continue
			}
			monDay = true
		}
		if !monDay {
			errc <- errors.New("Not an order day")
			bansc <- b
			return
		}

		bans := b

		for vend, mon := range j.monDir {
			for sku, monSKU := range mon.SKUs {
				if monSKU.Pending {
					continue
				}

				days := max(int(j.utc.Sub(monSKU.LastUTC).Hours()/24+0.5), 1)
				f := float64(monSKU.Sold) / float64(days)
				rp := (mon.AvgWait + float64(v.settings[vend].ReordPtAdd)) * f
				if float64(v.onHand[sku]) > rp {
					continue
				}

				bun := bans[vend]
				for i, ban := range bun {
					if ban.SKUPC != sku {
						continue
					}

					ban.Quantity = int(f * float64(min(monSKU.Sold, j.cfgFile.OrdXDaysWorth)))

					// save the banana in the bunch
					bun[i] = ban
				}

				// save the bunch in the bananas
				bans[vend] = bun
			}
		}

		errc <- nil
		bansc <- bans
	}()

	return bansc, errc
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
