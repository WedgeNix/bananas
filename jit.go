package bananas

import (
	"os"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/awsapi/file"
	"github.com/WedgeNix/awsapi/types"
)

type jit struct {
	ac         *awsapi.Controller
	sc         *skuvault.Ctr
	monDir     dir.Monitor
	monDirName string
}

// create a new just-in-time handler
func newJIT() (*jit, error) {
	j := jit{monDirName: os.Getenv("MONITOR_DIR")}
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
	return j.ac.OpenDir(j.monDirName, j.monDir)
}

// record all or no changes made to the monitor directory
func (j *jit) saveAWSChanges() error {
	return j.ac.SaveDir(j.monDir)
}

// combs through the orders and updates entries in the AWS monitor directory
func (j *jit) updateAWS(v *Vars, ords []order) error {
	pay := skuvault.GetProducts{
		PageSize: 10000,
	}

	// go through and see if any entries need to be pulled from SkuVault
	for _, ord := range ords {
		for _, itm := range ord.Items {
			vend := v.getVend(itm)

			// determine existence of vendor monitor file
			vendMon, exists := j.monDir[vend]
			if !exists {
				j.monDir[vend] = file.Monitor{}
			}

			// determine existence of item monitor SKU
			_, exists = vendMon[itm.SKU]
			if exists {
				continue
			}

			pay.ProductSKUs = append(pay.ProductSKUs, itm.SKU)
			vendMon[itm.SKU] = types.MonitorSKU{}
		}
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

				//
				// implement order time difference frequency calculations to set days and sold
				//

				monSKU := types.MonitorSKU{
					Then: prod.CreatedDateUtc,
				}

				// actually overwrite the empty monitor SKU
				j.monDir[v.getVend(itm)][prod.Sku] = monSKU
			}
		}
	}

	return nil
}
