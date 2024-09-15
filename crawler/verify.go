package crawler

import (
	"net/http"
	"time"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/service"
	"github.com/parnurzeal/gorequest"
)

var verifyJob chan proxyinabox.Proxy
var proxyServiceInstance proxyinabox.ProxyService

// Init crawler
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

// Verify verify proxies in database
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
		_, _, errs := gorequest.New().Timeout(time.Second*5).Retry(3, time.Second*2, http.StatusInternalServerError).Proxy(proxy).Get("https://api.myip.la/cn?json").EndStruct(&resp)
		delay := time.Now().Unix() - start
		if len(errs) != 0 || resp.IP != p.IP {
			proxyinabox.CI.DeleteProxy(p)
			continue
		}
		proxyinabox.DB.Model(&p).Update("delay", delay)
	}
}
