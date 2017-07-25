package worker

import (
	"encoding/json"
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
	ac := awsapi.New()

	var monDir dir.Monitor
	err := ac.OpenDir(``, monDir)
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
		FromDate:           time.Now().Add(-30 * time.Hour),
		ToDate:             time.Now(),
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

	for vend, file := range monDir {
		for sku, mon := range file {
			if !strings.Contains(scanned, sku) {
				continue
			}
			mon.Pending = false

			// overwrite on SKU for monitor file
			file[sku] = mon
		}

		// overwrite on vendor file for monitor directory
		monDir[vend] = file
	}

	// save changes on AWS
	err = ac.SaveDir(monDir)
	if err != nil {
		return err
	}

	util.HTTPStatus(c, http.StatusOK, "Successfully removed 'pending' from SKUs on AWS")

	return nil
}
