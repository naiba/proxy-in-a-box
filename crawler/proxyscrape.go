package crawler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/naiba/proxyinabox"
)

type proxyScrape struct {
}

type proxyScrapeResp struct {
	ShownRecords int  `json:"shown_records"`
	TotalRecords int  `json:"total_records"`
	Limit        int  `json:"limit"`
	Skip         int  `json:"skip"`
	Nextpage     bool `json:"nextpage"`
	Proxies      []struct {
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
		Ssl      bool   `json:"ssl"`
		IP       string `json:"ip"`
	} `json:"proxies"`
}

func newProxyScrape() *proxyScrape {
	return new(proxyScrape)
}

func (k *proxyScrape) Fetch() error {
	for {
		time.Sleep(time.Second * 3)
		body, err := getDocFromURL("https://api.proxyscrape.com/v3/free-proxy-list/get?request=displayproxies&proxy_format=protocolipport&format=json")
		if err != nil {
			fmt.Printf("[PIAB] proxyscrape [❎] crawler %v\n", err)
			continue
		}
		var resp proxyScrapeResp
		if err = json.Unmarshal([]byte(body), &resp); err != nil {
			fmt.Printf("[PIAB] proxyscrape [❎] crawler %v\n", err)
			continue
		}
		for _, p := range resp.Proxies {
			validateJobs <- proxyinabox.Proxy{
				IP:   p.IP,
				Port: fmt.Sprintf("%d", p.Port),
			}
		}
		fmt.Printf("[PIAB] proxyscrape [✅] crawler find %d proxies\n", len(resp.Proxies))
		time.Sleep(time.Minute * 5)
	}
}
