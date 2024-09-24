package crawler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/naiba/proxyinabox"
)

type monosansProxyList struct {
}

func newMonosansProxyList() *monosansProxyList {
	return &monosansProxyList{}
}

func (m *monosansProxyList) Fetch() {
	for {
		time.Sleep(time.Second * 3)
		body, err := getDocFromURL("https://raw.githubusercontent.com/monosans/proxy-list/main/proxies.json")
		if err != nil {
			fmt.Printf("[PIAB] monosansProxyList [❎] crawler %v\n", err)
			continue
		}
		var resp []struct {
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		}
		if err = json.Unmarshal([]byte(body), &resp); err != nil {
			fmt.Printf("[PIAB] monosansProxyList [❎] crawler body: %s, err: %v\n", body, err)
			continue
		}
		fmt.Printf("[PIAB] monosansProxyList [✅] crawler find %d proxies\n", len(resp))
		for _, p := range resp {
			validateJobs <- proxyinabox.Proxy{
				IP:       p.Host,
				Port:     fmt.Sprintf("%d", p.Port),
				Protocol: p.Protocol,
				Platform: proxyinabox.PlatformMonosansProxyList,
			}
		}
		time.Sleep(time.Minute * 3)
	}
}
