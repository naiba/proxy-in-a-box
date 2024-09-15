package crawler

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/naiba/proxyinabox"
)

var parseIpList = regexp.MustCompile(`fpsList = (.*);\n*.*totalCount\s=\s'(\d*)\';`)

// kuaiDaili 快代理
type kuaiDaili struct {
	urls []string
}

type kuaiProxyItem struct {
	IP   string
	Port string
}

func newKuaiDaili() *kuaiDaili {
	this := new(kuaiDaili)
	this.urls = []string{
		"https://www.kuaidaili.com/free/inha/",
		"https://www.kuaidaili.com/free/intr/",
	}
	return this
}

// Fetch fetch all proxies
func (k *kuaiDaili) Fetch() error {
	for _, pageURL := range k.urls {
		var currPageNo = 1
		var count int
		var ended bool
		for !ended {
			time.Sleep(time.Second * 3)
			body, err := getDocFromURL(pageURL + strconv.Itoa(currPageNo))
			if err != nil {
				fmt.Println("[PIAB]", "kuai", "[❎]", "crawler", err)
				continue
			}
			matches := parseIpList.FindStringSubmatch(body)
			if len(matches) < 3 {
				fmt.Println("[PIAB]", "kuai", "[❎]", "crawler", "parse error")
				continue
			}

			proxyListJson := matches[1]
			totalCount, err := strconv.Atoi(matches[2])
			if err != nil {
				fmt.Println("[PIAB]", "kuai", "[❎]", "crawler", err)
				continue
			}

			var proxyList []kuaiProxyItem
			if err = json.Unmarshal([]byte(proxyListJson), &proxyList); err != nil {
				fmt.Println("[PIAB]", "kuai", "[❎]", "crawler", err)
				continue
			}

			for _, p := range proxyList {
				validateJobs <- proxyinabox.Proxy{
					IP:       p.IP,
					Port:     p.Port,
					Platform: proxyinabox.PlatformKuai,
				}
			}

			count += len(proxyList)

			ended = count >= totalCount
			currPageNo++

			fmt.Println("[PIAB]", "kuai", "[🍾]", "crawler", len(proxyList), "proxies.")
		}
	}
	return nil
}
