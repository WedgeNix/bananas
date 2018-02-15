package bananas

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/WedgeNix/util"
)

func (v *Vars) getPage(page int, pay *payload, a, b time.Time) (int, int, error) {
	query := url.Values(map[string][]string{})
	query.Set(`page`, itoa(page))
	query.Set(`createDateStart`, a.Format(ssDateFmt))
	query.Set(`createDateEnd`, b.Format(ssDateFmt))
	query.Set(`pageSize`, `500`)

	resp, err := v.login.Get(shipURL + `orders?` + query.Encode())
	if err != nil {
		return 0, 0, util.Err(err)
	}
	fmt.Println(shipURL + `orders?` + query.Encode())
	fmt.Println(resp.Status)

	err = json.NewDecoder(resp.Body).Decode(pay)
	if err != nil {
		return 0, 0, util.Err(err)
	}
	defer resp.Body.Close()
	// fmt.Println(*pay)

	remaining := resp.Header.Get("X-Rate-Limit-Remaining")
	reqs, err := strconv.Atoi(remaining)
	if err != nil {
		return 0, 0, util.Err(err)
	}
	reset := resp.Header.Get("X-Rate-Limit-Reset")
	secs, err := strconv.Atoi(reset)
	if err != nil {
		return reqs, 0, util.Err(err)
	}

	return reqs, secs, nil
}
