package query

import (
	"net/url"
	"strconv"
	"time"
)

type Q url.Values

func (q Q) SetTime(key string, t time.Time, layout string) {
	if t != (time.Time{}) {
		q.Set(key, t.Format(layout))
	}
}
func (q Q) SetInt(key string, i int) {
	if i != 0 {
		q.Set(key, strconv.Itoa(i))
	}
}
func (q Q) Set(key, val string) {
	if len(val) > 0 {
		url.Values(q).Set(key, val)
	}
}
func (q Q) Encode() string {
	return url.Values(q).Encode()
}
