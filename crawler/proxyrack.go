package crawler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/naiba/proxyinabox"
)

type proxyRack struct {
}

type proxyRackResp struct {
	QueryRecordCount int `json:"query_record_count"`
	TotalRecordCount int `json:"total_record_count"`
	Records          []struct {
		IP       string `json:"ip"`
		Port     string `json:"port"`
		Protocol string `json:"protocol"`
	} `json:"records"`
}

func newProxyRack() *proxyRack {
	return new(proxyRack)
}

func (k *proxyRack) Fetch() {
	currentPage := 1
	for {
		time.Sleep(time.Second * 3)
		body, err := getDocFromURL("https://proxyfinder.proxyrack.com/proxies.json?perPage=500&offset="+strconv.Itoa((currentPage-1)*500),
			http.Header{
				"origin": {"https://proxyrack.com"},
			},
		)
		if err != nil {
			fmt.Printf("[PIAB] proxyrack [❎] crawler %v\n", err)
			continue
		}
		var resp proxyRackResp
		if err = json.Unmarshal([]byte(body), &resp); err != nil {
			fmt.Printf("[PIAB] proxyrack [❎] crawler %v\n", err)
			continue
		}
		fmt.Printf("[PIAB] proxyrack [✅] crawler find %d proxies\n", len(resp.Records))
		for _, p := range resp.Records {
			validateJobs <- proxyinabox.Proxy{
				IP:       p.IP,
				Port:     p.Port,
				Platform: proxyinabox.PlatformProxyRack,
				Protocol: p.Protocol,
			}
		}
		if len(resp.Records) == 0 || currentPage*500 >= resp.TotalRecordCount {
			currentPage = 1
		} else {
			currentPage++
		}
	}
}
