package worker

import (
	"bananas"
	"encoding/json"
	"strings"

	"time"

	"net/http"

	"github.com/OuttaLineNomad/skuvault"
	"github.com/WedgeNix/awsapi"
	"github.com/gin-gonic/gin"
	"github.com/mrmiguu/print"
	"github.com/mrmiguu/un"
)

// StartWorker montitors SKU Vault for POs that are recived and processed.
func StartWorker(c *gin.Context) {
	awsSess := awsapi.New()
	key := bananas.AwsDir + "/" + bananas.FileName
	awsMon := awsapi.ObjectMonitors{}
	exist := un.Bool(awsSess.Get(key, &awsMon))
	if !exist {
		print.Msg("No Such File in AWS.")
		c.JSON(http.StatusBadRequest, gin.H{
			"Status":  http.StatusOK,
			"Message": "No Such File in AWS.",
		})
		return
	}
	svSess := skuvault.NewEnvCredSession()
	pld := &skuvault.GetTransactions{
		WarehouseID:        34,
		TransactionType:    "Add",
		TransactionReasons: []string{"receiving"},
		FromDate:           time.Now().Add(-30 * time.Hour),
		ToDate:             time.Now(),
	}
	svResp := svSess.Inventory.GetTransactions(pld)

	b := un.Bytes(json.Marshal(svResp.Transactions))

	svTrans := string(b)

	for sku, obj := range awsMon {
		test := strings.Contains(svTrans, sku)
		if test {
			obj.Pending = false
		}
	}

	versionID := un.String(awsSess.Put(key, awsMon))

	print.Msg("VersionId: ", versionID)
	c.JSON(http.StatusOK, gin.H{
		"Status":  http.StatusOK,
		"Message": "VersionId: " + versionID,
	})
}
