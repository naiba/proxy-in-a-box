package crawler

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/naiba/proxyinabox"
)

type thespeedx struct {
}

func newTheSpeedX() *thespeedx {
	return new(thespeedx)
}

func (k *thespeedx) Fetch() {
	proxySources := []string{
		"https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt",
		"https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks4.txt",
		"https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt",
	}
	proxyProtocols := []string{
		"http",
		"socks4",
		"socks5",
	}
	for i := 0; i < len(proxySources); i++ {
		go func(pageURL string, protocol string) {
			for {
				time.Sleep(time.Second * 3)
				body, err := getDocFromURL(pageURL)
				if err != nil {
					fmt.Printf("[PIAB] thespeedx [❎] crawler %v\n", err)
					continue
				}
				var validProxies int
				proxies := strings.Split(body, "\n")
				for _, p := range proxies {
					host, port, err := net.SplitHostPort(p)
					if err != nil {
						continue
					}
					validProxies++
					validateJobs <- proxyinabox.Proxy{
						IP:       host,
						Port:     port,
						Platform: proxyinabox.PlatformTheSpeedX,
						Protocol: protocol,
					}
				}
				fmt.Printf("[PIAB] thespeedx [✅] crawler find %d proxies\n", validProxies)
				time.Sleep(time.Minute * 5)
			}
		}(proxySources[i], proxyProtocols[i])
	}
}
