package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/robfig/cron"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/crawler"
	"github.com/naiba/proxyinabox/mitm"
	"github.com/naiba/proxyinabox/service"
)

var configFilePath, httpProxyAddr, httpsProxyAddr, manageAddr string
var m *mitm.MITM
var rootCmd = &cobra.Command{
	Use:   "proxy-in-a-box",
	Short: "Proxy-in-a-Box provide many proxies.",
	Long:  `Proxy-in-a-Box helps programmers quickly and easily develop powerful crawler services. one-script, easy-to-use: proxies in a box.`,
	Run: func(cmd *cobra.Command, args []string) {
		proxyinabox.Init(configFilePath)
		fmt.Println("[PIAB]", "main", "[😁]", proxyinabox.Config.Sys.Name, "v1.0.0")
		proxyinabox.CI = service.NewMemCache()

		crawler.Init()

		m = newMITM()
		m.Init()

		m.ServeHTTP()

		// 启动 pinchtab 子进程（如果配置了）
		if proxyinabox.Config.Pinchtab.Bin != "" {
			if err := crawler.StartPinchtab(); err != nil {
				fmt.Println("[PIAB]", "pinchtab", "[👻]", err)
			}
		}

		// 加载 YAML 驱动的 proxy 源并启动抓取
		sources, err := crawler.LoadSources("./data/sources")
		if err != nil {
			fmt.Println("[PIAB]", "panic", "[👻]", err)
			os.Exit(1)
		}
		crawler.FetchAllSources(sources)
		crawler.Verify()

		c := cron.New()
		c.AddFunc("0 "+strconv.Itoa(proxyinabox.Config.Sys.VerifyDuration)+" * * * *", crawler.Verify)
		c.Start()

		// 信号处理：统一的清理路径，确保 pinchtab 和 Chrome 子进程被完整回收
		// os.Exit 不会触发 defer，所以必须在信号处理中显式调用清理函数
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			fmt.Printf("[PIAB] received signal %v, shutting down...\n", sig)
			c.Stop()
			crawler.StopPinchtab()
			os.Exit(0)
		}()

		managerHttpServer := http.NewServeMux()
		managerHttpServer.HandleFunc("/stat", func(w http.ResponseWriter, r *http.Request) {
			pl := proxyinabox.CI.ProxyLength()
			w.Write([]byte(fmt.Sprintf("ProxyInABox\n\nHTTP: %s\nHTTPS: %s\n\nAvailable: %d\n", httpProxyAddr, httpsProxyAddr, pl)))
		})
		managerHttpServer.HandleFunc("/get", func(w http.ResponseWriter, r *http.Request) {
			proxy, ok := proxyinabox.CI.GetProxy()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte("No proxy available"))
			} else {
				w.Write([]byte(proxy))
			}
		})

		// Dashboard HTML 页面
		managerHttpServer.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// 仅响应根路径，避免拦截其他未注册路径
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(dashboardHTML))
		})

		// API: 代理池统计摘要
		managerHttpServer.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
			proxies := proxyinabox.CI.GetAllProxies()
			byProtocol := make(map[string]int)
			bySource := make(map[string]int)
			for _, p := range proxies {
				proto := strings.ToLower(p.Protocol)
				if proto == "" {
					proto = "http"
				}
				byProtocol[proto]++
				bySource[p.Source]++
			}
			stats := map[string]interface{}{
				"total":       len(proxies),
				"by_protocol": byProtocol,
				"by_source":   bySource,
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(stats)
		})

		// API: 全量代理列表
		managerHttpServer.HandleFunc("/api/proxies", func(w http.ResponseWriter, r *http.Request) {
			proxies := proxyinabox.CI.GetAllProxies()
			type proxyJSON struct {
				IP         string `json:"ip"`
				Port       string `json:"port"`
				Protocol   string `json:"protocol"`
				Country    string `json:"country"`
				Source     string `json:"source"`
				Delay      int64  `json:"delay"`
				LastVerify string `json:"last_verify"`
			}
			result := make([]proxyJSON, len(proxies))
			for i, p := range proxies {
				result[i] = proxyJSON{
					IP:         p.IP,
					Port:       p.Port,
					Protocol:   p.Protocol,
					Country:    p.Country,
					Source:     p.Source,
					Delay:      p.Delay,
					LastVerify: p.LastVerify.Format("2006-01-02T15:04:05Z"),
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		})

		// API: 各源抓取状态
		managerHttpServer.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(crawler.GetSourceStatuses())
		})
		if err := http.ListenAndServe(manageAddr, managerHttpServer); err != nil {
			fmt.Println("[PIAB]", "panic", "[👻]", err)
			os.Exit(1)
		}
	},
	PreRun: func(cmd *cobra.Command, args []string) {
		viper.SetConfigType("yaml")
		viper.SetConfigFile(configFilePath)
		if err := viper.ReadInConfig(); err != nil {
			panic(err)
		}
		if err := viper.Unmarshal(&proxyinabox.Config); err != nil {
			panic(err)
		}
	},
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&configFilePath, "conf", "c", "./data/pb.yaml", "config file")
	rootCmd.PersistentFlags().StringVarP(&httpProxyAddr, "ha", "p", "0.0.0.0:8080", "http proxy server addr")
	rootCmd.PersistentFlags().StringVarP(&httpsProxyAddr, "sa", "s", "0.0.0.0:8081", "https proxy server addr")
	rootCmd.PersistentFlags().StringVarP(&manageAddr, "ma", "m", "0.0.0.0:8083", "management/dashboard addr")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("[PIAB]", "panic", "[👻]", err)
		os.Exit(1)
	}
}

func newMITM() *mitm.MITM {
	m := &mitm.MITM{
		ListenHTTPS: proxyinabox.Config.EnableMITM,
		EnableMITM:  proxyinabox.Config.EnableMITM,
		HTTPAddr:    httpProxyAddr,
		HTTPSAddr:   httpsProxyAddr,
		Scheduler:   proxyinabox.CI.PickProxy,
		Filter: func(req *http.Request) error {
			if !proxyinabox.CI.IPLimiter(req) {
				return fmt.Errorf("%s", "请求次数过快")
			}
			if !proxyinabox.CI.HostLimiter(req) {
				return fmt.Errorf("%s", "请求域名过多")
			}
			return nil
		},
	}
	if proxyinabox.Config.EnableMITM {
		m.TLSConf = &mitm.TLSConfig{
			PrivateKeyFile: "proxyinabox.key",
			CertFile:       "proxyinabox.pem",
		}
	}
	return m
}
