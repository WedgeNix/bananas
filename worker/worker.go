package worker

import (
	"encoding/json"
	"math"
	"strings"

	"time"

	"net/http"

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
		return err
	}

	monDir := dir.BananasMon{}
	err = ac.OpenDir(dir.BananasMonName, monDir)
	if err != nil {
		return err
	}

	if len(monDir) < 1 {
		util.HTTPStatus(c, http.StatusNoContent, "No files found in AWS")
		return nil
	}

	sc := skuvault.NewEnvCredSession()

	pld := &skuvault.GetTransactions{
		WarehouseID:        34,
		TransactionType:    "Add",
		TransactionReasons: []string{"receiving"},
		FromDate:           util.LANow().Add(-26 * time.Hour),
		ToDate:             util.LANow(),
		PageNumber:         1,
	}
	var scanned string
	for {
		resp := sc.Inventory.GetTransactions(pld)
		pld.PageNumber++

		// this is the end of our pages
		if len(resp.Transactions) < 1 {
			break
		}

		b, err := json.Marshal(resp.Transactions)
		if err != nil {
			return err
		}

		scanned += string(b)
	}

	for vend, mon := range monDir {
		for sku, monSKU := range mon.SKUs {
			if !strings.Contains(scanned, sku) {
				continue
			}
			monSKU.LastUTC = time.Now().UTC()

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
		return err
	}

	util.HTTPStatus(c, http.StatusOK, "Successfully removed 'pending' from SKUs on AWS")

	return nil
}
