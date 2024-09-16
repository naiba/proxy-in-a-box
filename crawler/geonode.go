package crawler

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/naiba/proxyinabox"
)

type geonodeResp struct {
	Data []struct {
		IP        string   `json:"ip"`
		Port      string   `json:"port"`
		Protocols []string `json:"protocols"`
	} `json:"data"`
	Total int `json:"total"`
	Page  int `json:"page"`
	Limit int `json:"limit"`
}

type geonode struct {
}

func newGeoNode() *geonode {
	return new(geonode)
}

func (k *geonode) Fetch() {
	currentPage := 1
	for {
		time.Sleep(time.Second * 3)
		body, err := getDocFromURL("https://proxylist.geonode.com/api/proxy-list?limit=500&sort_by=lastChecked&sort_type=desc&page=" + strconv.Itoa(currentPage))
		if err != nil {
			fmt.Printf("[PIAB] geonode [❎] crawler %v\n", err)
			continue
		}
		var resp geonodeResp
		if err = json.Unmarshal([]byte(body), &resp); err != nil {
			fmt.Printf("[PIAB] geonode [❎] crawler %v\n", err)
			continue
		}
		for _, p := range resp.Data {
			validateJobs <- proxyinabox.Proxy{
				IP:       p.IP,
				Port:     p.Port,
				Platform: proxyinabox.PlatformGeoNode,
				Protocol: p.Protocols[0],
			}
		}
		if currentPage*resp.Limit >= resp.Total {
			currentPage = 1
		} else {
			currentPage++
		}
		fmt.Printf("[PIAB] geonode [✅] crawler find %d proxies\n", len(resp.Data))
	}
}
