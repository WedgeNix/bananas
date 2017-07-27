package bananas

import (
	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/ext"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/util"
)

type jit struct {
	ac      *awsapi.Controller
	sc      *skuvault.Ctr
	monDir  dir.BananasMon
	cfgFile file.BananasCfg
}

// create a new just-in-time handler
func newJIT() (*jit, error) {
	j := jit{}
	ac, err := awsapi.New()
	if err != nil {
		return &j, err
	}
	j.ac = ac
	j.sc = skuvault.NewEnvCredSession()
	return &j, nil
}

// read in the entire monitor directory
func (j *jit) readAWS() error {
	exists, err := j.ac.Open(awsapi.BananasCfgFile, &j.cfgFile)
	if err != nil {
		return err
	}
	if !exists {
		j.cfgFile.Last = time.Now().UTC()
	}

	return j.ac.OpenDir(awsapi.BananasMonDir, j.monDir)
}

// record all or no changes made to the monitor directory
func (j *jit) saveAWSChanges() error {
	err := j.ac.Save(awsapi.BananasCfgFile, j.cfgFile)
	if err != nil {
		return err
	}
	return j.ac.SaveDir(j.monDir)
}

// combs through the orders and updates entries in the AWS monitor directory
func (j *jit) updateAWS(v *Vars, ords []order) error {
	ords = j.monitorsOnly(v, ords)
	newSKUs := make(map[string]interface{})

	// go through and see if any entries need to be pulled from SkuVault
	// also increase sold count once for each time the SKU is found
	for _, ord := range ords {
		for _, itm := range ord.Items {
			vendFile := v.getVend(itm) + ext.BananasMon

			// determine existence of vendor monitor file
			vendMon, exists := j.monDir[vendFile]
			if !exists {
				j.monDir[vendFile] = file.BananasMon{}
			}

			// if monitor SKU doesn't exist then throw its SKU into
			// the payload for populating later
			monSKU, exists := vendMon.SKUs[itm.SKU]
			if !exists {
				newSKUs[itm.SKU] = nil
			}

			monSKU.Sold += itm.Quantity
			monSKU.Then = time.Now().UTC()

			// overwrite the changes on both a file level and directory level
			vendMon.SKUs[itm.SKU] = monSKU
			j.monDir[vendFile] = vendMon
		}
	}

	days := max(int(time.Now().UTC().Sub(j.cfgFile.Last).Hours()/24+0.5), 1)

	for vend, mon := range j.monDir {
		for sku, monSKU := range mon.SKUs {
			monSKU.Days += days

			// save changes on monitor SKU level
			mon.SKUs[sku] = monSKU
		}

		// save changes on monitor vendor level
		j.monDir[vend] = mon
	}

	pay := skuvault.GetProducts{
		PageSize: 10000,
	}

	// fill up non-repeating SKUs into payload's product SKU list
	for sku := range newSKUs {
		pay.ProductSKUs = append(pay.ProductSKUs, sku)
	}

	// these are all brand new entries on the AWS database
	resp := j.sc.Products.GetProducts(&pay)
	for _, err := range resp.Errors {
		err := err.(error)
		if err == nil {
			continue
		}
		return err
	}

	// update all monitor SKUs from response products
	for _, prod := range resp.Products {
		for _, ord := range ords {
			for _, itm := range ord.Items {
				if itm.SKU != prod.Sku {
					continue
				}

				monSKU := j.monDir[v.getVend(itm)].SKUs[prod.Sku]
				monSKU.Then = prod.CreatedDateUtc

				// actually overwrite the empty monitor SKU
				j.monDir[v.getVend(itm)].SKUs[prod.Sku] = monSKU
			}
		}
	}

	return nil
}

func (j *jit) monitorsOnly(v *Vars, ords []order) []order {
	filtOrds := ords
	for _, ord := range ords {
		for _, itm := range ord.Items {
			if !v.isMon(itm) {
				continue
			}
			filtOrds = append(filtOrds, ord)
		}
	}
	return filtOrds
}

// adjusts quantity in bananas for individual SKUs based on AWS data
func (j *jit) emailOrders(v *Vars, bans bananas) {
	if day := util.LANow().Weekday(); day == time.Monday || day == time.Thursday {
		return
	}
	// for _, set := range v.settings {
	// 	if set.
	// }
	// for vend, bun := range bans {
	// 	for _, ban := range bun {
	// 		ban.
	// 	}
	// }
}

func max(i, j int) int {
	if i > j {
		return i
	}
	return j
}
