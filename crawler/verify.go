package crawler

import (
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService

func Init() {
	initV()
	initC()
}

func initV() {
	proxyServiceInstance = &service.ProxyService{DB: proxyinabox.DB}
	verifyJob = make(chan proxyinabox.Proxy, proxyinabox.Config.Sys.ProxyVerifyWorker)
	for i := 0; i < proxyinabox.Config.Sys.ProxyVerifyWorker; i++ {
		go getDelay(verifyJob)
	}
}

func Verify() {
	list, _ := proxyServiceInstance.GetUnVerified()
	for _, p := range list {
		verifyJob <- p
	}
}

func getDelay(pc chan proxyinabox.Proxy) {
	for p := range pc {
		proxy := p.URI()
		start := time.Now().Unix()
		var resp validateJSON
		_, err := getURLThroughProxyWithRetry("https://api.myip.la/cn?json", time.Second*5, proxy, 3)
		delay := time.Now().Unix() - start
		if err != nil || resp.IP != p.IP {
			proxyinabox.CI.DeleteProxy(p)
			continue
		}
		proxyinabox.DB.Model(&p).Update("delay", delay)
	}
}
