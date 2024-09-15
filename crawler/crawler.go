package crawler

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/parnurzeal/gorequest"
)

var validateJobs chan proxyinabox.Proxy
var pendingValidate sync.Map

type validateJSON struct {
	IP       string
	Location struct {
		City        string
		CountryCode string `json:"country_code"`
		CountryName string `json:"country_name"`
		Latitude    string
		Longitude   string
		Province    string
	}
}

func initC() {
	validateJobs = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker*2)
	for i := 1; i <= proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go validator(i, validateJobs)
	}
}

func getDocFromURL(url string) (string, error) {
	client := gorequest.New().
		Retry(3, time.Second*3).
		Timeout(time.Second * 20)
	if p, ok := proxyinabox.CI.RandomProxy(); ok {
		client.Proxy(p)
	}
	_, body, errs := client.
		TLSClientConfig(&tls.Config{InsecureSkipVerify: true}).
		Get(url).
		Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36").
		End()
	if len(errs) > 0 {
		return "", errs[0]
	}
	return body, nil
}

// FetchProxies fetch new proxies
func FetchProxies() {
	cs := []proxyinabox.ProxyCrawler{
		newKuaiDaili(),
		newProxyScrape(),
	}
	for _, c := range cs {
		go c.Fetch()
	}
}

func validator(id int, validateJobs chan proxyinabox.Proxy) {
	for p := range validateJobs {
		// format
		p.IP = strings.TrimSpace(p.IP)
		proxy := p.URI()
		// is processing
		_, has := pendingValidate.Load(proxy)
		if !has && !proxyinabox.CI.HasProxy(p.URI()) {
			pendingValidate.Store(proxy, nil)
			var resp validateJSON
			start := time.Now().Unix()
			_, _, errs := gorequest.New().Timeout(time.Second*7).Retry(3, time.Second*2, http.StatusInternalServerError).Proxy(proxy).Get("https://api.myip.la/cn?json").EndStruct(&resp)
			if len(errs) == 0 && resp.IP == p.IP {
				p.Country = resp.Location.CountryName
				p.Provence = resp.Location.Province
				p.Delay = time.Now().Unix() - start
				p.LastVerify = time.Now()

				if e := proxyinabox.CI.SaveProxy(p); e == nil {
					if proxyinabox.Config.Debug {
						fmt.Println("[PIAB]", "crawler", "[✅]", id, "find a available proxy", p)
					}
				} else {
					fmt.Println("[PIAB]", "crawler", "[❎]", id, "error save proxy", e.Error())
				}
			}
			pendingValidate.Delete(proxy)
		}
	}
}
