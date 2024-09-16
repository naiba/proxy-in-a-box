package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

func main() {
	var wg sync.WaitGroup
	httpClient := getProxyClient("http://127.0.0.1:8080")
	wg.Add(20)
	go func() {
		for i := 0; i < 10; i++ {
			testHTTPGet("HTTP-"+strconv.Itoa(i), "http://www.baidu.com", httpClient)
			wg.Done()
		}
	}()
	httpsClient := getProxyClient("https://127.0.0.1:8081")
	go func() {
		for i := 0; i < 10; i++ {
			testHTTPGet("HTTPS-"+strconv.Itoa(i), "https://www.baidu.com/", httpsClient)
			wg.Done()
		}
	}()
	wg.Wait()
}

func getProxyClient(proxyURL string) *http.Client {
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		panic(err)
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxy),
		}}
}

func testHTTPGet(msg, url string, c *http.Client) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Println(msg, "ERROR", err)
		return
	}
	// set authorized header
	if strings.HasPrefix(url, "http://") {
		req.Header.Set("Naiba", "lifelonglearning")
	}
	resp, err := c.Do(req)
	if err != nil {
		log.Println(msg, "ERROR", err)
		return
	}
	var body []byte
	defer resp.Body.Close()
	body, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("[proxy-in-a-box Example]", "ioutil.ReadAll", "[❎]", err)
		return
	}
	fmt.Println("[Example]", msg, "[📮]", resp.StatusCode, resp.Header, len(body))
}
