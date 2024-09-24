package crawler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
	utls "github.com/refraction-networking/utls"
	"github.com/wabarc/proxier"
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

func getDocFromURL(url string, customHeaders ...http.Header) (string, error) {
	proxy, _ := proxyinabox.CI.RandomProxy()
	body, err := getURLThroughProxyWithRetry(url, time.Second*20, proxy, 3, customHeaders...)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func FetchProxies() {
	cs := []proxyinabox.ProxyCrawler{
		newKuaiDaiLi(),
		newProxyScrape(),
		newGeoNode(),
		newTheSpeedX(),
		newAdvancedName(),
		newSpysMe(),
		newProxyRack(),
		newIPRoyal(),
		newMonosansProxyList(),
	}
	for _, c := range cs {
		go c.Fetch()
	}
}

func validator(id int, validateJobs chan proxyinabox.Proxy) {
	for p := range validateJobs {
		p.IP = strings.TrimSpace(p.IP)
		proxy := p.URI()
		_, has := pendingValidate.Load(proxy)
		if !has && !proxyinabox.CI.HasProxy(p.URI()) {
			pendingValidate.Store(proxy, nil)
			start := time.Now().Unix()

			var resp validateJSON
			body, err := getURLThroughProxyWithRetry("https://api.myip.la/cn?json", time.Second*7, proxy, 3)
			if err == nil {
				err = json.Unmarshal([]byte(body), &resp)
			}

			if err == nil && resp.IP == p.IP {
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

func getURLThroughProxyWithRetry(u string, timeout time.Duration, proxy string, retry int, customHeaders ...http.Header) ([]byte, error) {
	var opts = []proxier.UTLSOption{
		proxier.ClientHello(&utls.HelloChrome_Auto),
		proxier.Config(&utls.Config{
			InsecureSkipVerify: true,
		}),
	}
	if proxy != "" {
		proxyUrl, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}
		opts = append(opts, proxier.Proxy(proxyUrl))
	}
	roundTripper, err := proxier.NewUTLSRoundTripper(opts...)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{
		Timeout:   timeout,
		Transport: roundTripper,
	}
	request, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36")
	for _, h := range customHeaders {
		for k, v := range h {
			request.Header.Set(k, strings.Join(v, ";"))
		}
	}
	var lastErr error
	for i := 0; i < retry; i++ {
		resp, err := httpClient.Do(request)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}
