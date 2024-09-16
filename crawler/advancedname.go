package crawler

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/naiba/proxyinabox"
)

type advancedname struct{}

func newAdvancedName() *advancedname {
	return new(advancedname)
}

func (k *advancedname) Fetch() {
	urls := []string{
		"https://advanced.name/freeproxy/66e7be802de75?type=http",
		"https://advanced.name/freeproxy/66e7be802de75?type=https",
	}
	for _, u := range urls {
		go func(pageURL string, protocol string) {
			for {
				time.Sleep(time.Second * 3)
				body, err := getDocFromURL(pageURL)
				if err != nil {
					fmt.Printf("[PIAB] advancedname [❎] crawler %v\n", err)
					continue
				}
				proxies := strings.Split(body, "\n")
				var validProxies []proxyinabox.Proxy
				for _, p := range proxies {
					host, port, err := net.SplitHostPort(p)
					if err != nil {
						continue
					}
					validProxies = append(validProxies, proxyinabox.Proxy{
						IP:       host,
						Port:     port,
						Protocol: protocol,
						Platform: proxyinabox.PlatformAdvancedName,
					})
				}
				fmt.Printf("[PIAB] advancedname [✅] crawler find %d proxies\n", len(validProxies))
				for _, p := range validProxies {
					validateJobs <- p
				}
				time.Sleep(time.Minute * 5)
			}
		}(u, strings.Split(u, "type=")[1])
	}
}
