# Proxy-in-a-Box

[![Go Report Card](https://goreportcard.com/badge/github.com/naiba/proxyinabox)](https://goreportcard.com/report/github.com/naiba/proxyinabox) [![travis](https://travis-ci.com/naiba/proxyinabox.svg?branch=master)](https://travis-ci.com/naiba/proxyinabox)

Proxy-in-a-Box helps programmers quickly and easily develop powerful crawler services. one-script, easy-to-use: proxies in a box.

```shell
Usage:
  proxy-in-a-box [flags]

Flags:
  -c, --conf string   config file (default "./pb.yaml")
  -p, --ha string     http proxy server addr (default "127.0.0.1:8080")
  -h, --help          help for proxy-in-a-box
  -s, --sa string     https proxy server addr (default "127.0.0.1:8081")
```

## Usage

1. get lastest Proxy-in-a-Box
    ```shell
    go get -u -v github.com/naiba/proxyinabox/cmd/proxy-in-a-box/...
    ```
2. enter the application directory
    ```shell
    cd $GOPATH/bin
    ```
3. write config file #Config
4. run it
    ```shell
    ./proxy-in-a-box
    ```
5. configured in your code
    ```none
    HTTP proxy: `http://[IP]:8080`
    HTTPS proxy: `https://[IP]:8081`
    * Please set http header when requesting: "Naiba: lifelonglearning" ref:https://github.com/naiba/proxyinabox/blob/master/cmd/proxy-in-a-box/test_server.sh
    ```
    Set in the code, and then grab it, the **Proxy-in-a-Box** will automatically assign the proxy.

## Config

```yaml
# run in debug mode
debug: true
# system config
sys:
  name: Naiba
  # verify proxy's worker num
  proxy_verify_worker: 20
  # how many domains can request per ip in 30min
  domains_per_ip: 30
  # how many request can do per ip in 1s
  request_limit_per_ip: 10
  # verify interval of the proxy stored in the database
  verify_duration: 30
```

## Benchmark

```shell
ab -H 'Naiba: lifelonglearning' -v4  -n100 -c10 -X 127.0.0.1:8080 http://api.ip.la/cn
```