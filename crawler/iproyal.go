package crawler

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/naiba/proxyinabox"
)

type iproyal struct {
}

func newIPRoyal() *iproyal {
	return new(iproyal)
}

func (k *iproyal) Fetch() {
	currentPage := 1
	for {
		time.Sleep(time.Second * 3)
		body, err := getDocFromURL("https://iproyal.com/free-proxy-list/?entries=200&page=" + strconv.Itoa(currentPage))
		if err != nil {
			fmt.Printf("[PIAB] iproyal [❎] crawler %v\n", err)
			continue
		}
		doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
		if err != nil {
			fmt.Printf("[PIAB] iproyal [❎] crawler %v\n", err)
			continue
		}
		var ps []proxyinabox.Proxy
		var p *proxyinabox.Proxy
		doc.Find("div.flex.items-center.astro-lmapxigl").Each(func(i int, s *goquery.Selection) {
			if p != nil {
				if p.Port != "" {
					p.Protocol = s.Text()
					ps = append(ps, *p)
					p = nil
					return
				}
				p.Port = s.Text()
				return
			}
			ip := net.ParseIP(s.Text())
			if ip != nil {
				p = &proxyinabox.Proxy{
					IP:       ip.String(),
					Platform: proxyinabox.PlatformIPRoyal,
				}
			}
		})
		for _, p := range ps {
			validateJobs <- p
		}
		fmt.Printf("[PIAB] iproyal [✅] crawler find %d proxies at page %d\n", len(ps), currentPage)
		if _, ok := doc.Find("span.pagination-link").Last().Attr("disabled"); ok {
			currentPage = 1
		} else {
			currentPage++
		}
	}
}
