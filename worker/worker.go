package worker

import (
	"encoding/json"
	"math"
	"strings"

	"time"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/WedgeNix/awsapi/dir"
	"github.com/WedgeNix/util"
	"github.com/gin-gonic/gin"
)

// StartWorker montitors SKU Vault for POs that are recived and processed.
func StartWorker(c *gin.Context) error {
	ac, err := awsapi.New()
	if err != nil {
		return util.Err(err)
	}

	monDir := dir.BananasMon{}
	err = ac.OpenDir(dir.BananasMonName, monDir)
	if err != nil {
		return util.Err(err)
	}

	if len(monDir) < 1 {
		util.Log("No files found in AWS")
		return nil
	}

	sc := skuvault.NewEnvCredSession()

	pld := &skuvault.GetTransactions{
		WarehouseID:        34,
		TransactionType:    "Add",
		TransactionReasons: []string{"receiving"},
		FromDate:           util.LANow().Add(-26 * time.Hour),
		ToDate:             util.LANow(),
		PageSize:           10000,
	}
	util.Log(pld)
	var scanned string
	for {
		resp := sc.Inventory.GetTransactions(pld)
		pld.PageNumber++
		util.Log(resp)

		// this is the end of our pages
		if len(resp.Transactions) < 1 {
			break
		}

		b, err := json.Marshal(resp.Transactions)
		if err != nil {
			return util.Err(err)
		}

		scanned += string(b)
	}
	util.Log("pre scanned unloading")
	for vend, mon := range monDir {
		for sku, monSKU := range mon.SKUs {
			if !strings.Contains(scanned, sku) {
				continue
			}
			// forgiveness
			// monSKU.LastUTC = time.Now().UTC()

			mon.OrdSKUCnt++
			dayDiff := math.Max(util.LANow().Sub(monSKU.Pending).Hours()/24, 1)
			if monSKU.Pending.IsZero() {
				dayDiff = 5
			}
			rez := 1.0 / mon.OrdSKUCnt
			mon.AvgWait = mon.AvgWait*(1-rez) + dayDiff*rez

			monSKU.Pending = time.Time{}

			// overwrite on SKU for monitor file
			mon.SKUs[sku] = monSKU
		}

		// overwrite on vendor file for monitor directory
		monDir[vend] = mon
	}

	// save changes on AWS
	err = ac.SaveDir(dir.BananasMonName, monDir)
	if err != nil {
		return util.Err(err)
	}

	return nil
}
