package crawler

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/naiba/proxyinabox"
)

type spysme struct{}

func newSpysMe() *spysme {
	return new(spysme)
}

func (k *spysme) Fetch() {
	var urlSources = []string{
		"https://spys.me/proxy.txt",
		"https://spys.me/socks.txt",
	}
	var protocols = []string{
		"http",
		"socks5",
	}
	for i := 0; i < len(urlSources); i++ {
		go func(pageURL string, protocol string) {
			for {
				time.Sleep(time.Second * 3)
				body, err := getDocFromURL(pageURL)
				if err != nil {
					fmt.Printf("[PIAB] spysme [❎] crawler %v\n", err)
					continue
				}
				var validProxies int
				proxies := strings.Split(body, "\n")
				for _, p := range proxies {
					hostPort, _, ok := strings.Cut(p, " ")
					if !ok {
						continue
					}
					host, port, err := net.SplitHostPort(hostPort)
					if err != nil {
						continue
					}
					validProxies++
					validateJobs <- proxyinabox.Proxy{
						IP:       host,
						Port:     port,
						Platform: proxyinabox.PlatformSpysMe,
						Protocol: protocol,
					}
				}
				fmt.Printf("[PIAB] spysme [✅] crawler find %d proxies\n", validProxies)
				time.Sleep(time.Minute * 30)
			}
		}(urlSources[i], protocols[i])
	}
}
