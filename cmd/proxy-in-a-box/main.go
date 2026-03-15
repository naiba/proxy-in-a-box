package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/naiba/proxyinabox"
	"github.com/naiba/proxyinabox/crawler"
	"github.com/naiba/proxyinabox/mitm"
	"github.com/naiba/proxyinabox/service"
)

var configFilePath, httpProxyAddr, httpsProxyAddr, manageAddr string
var m *mitm.MITM
var version = "dev"
var testSourceVerifyWorkers int

var testSourceCmd = &cobra.Command{
	Use:   "test-source [yaml-file]",
	Short: "Test a single proxy source YAML file (fetch + verify availability)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		viper.SetConfigType("yaml")
		viper.SetConfigFile(configFilePath)
		if err := viper.ReadInConfig(); err != nil {
			fmt.Println("[PIAB] config error:", err)
			os.Exit(1)
		}
		if err := viper.Unmarshal(&proxyinabox.Config); err != nil {
			fmt.Println("[PIAB] config error:", err)
			os.Exit(1)
		}

		proxyinabox.Config.Debug = true
		crawler.Init()

		fileSources, err := crawler.LoadSources(filepath.Dir(args[0]))
		if err != nil {
			fmt.Println("[PIAB] load error:", err)
			os.Exit(1)
		}

		targetName := strings.TrimSuffix(filepath.Base(args[0]), ".yaml")
		var target *crawler.Source
		for i := range fileSources {
			if fileSources[i].Name == targetName {
				target = &fileSources[i]
				break
			}
		}
		if target == nil {
			fmt.Printf("[PIAB] source '%s' not found in %s\n", targetName, filepath.Dir(args[0]))
			os.Exit(1)
		}

		fmt.Printf("[PIAB] testing source: %s (type: %s)\n", target.Name, target.Type)
		proxies, err := crawler.TestFetchSource(*target)
		if err != nil {
			fmt.Printf("[PIAB] ❎ fetch error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[PIAB] ✅ fetched %d proxies\n", len(proxies))

		workers := testSourceVerifyWorkers
		if workers <= 0 {
			workers = 20
		}
		if workers > len(proxies) {
			workers = len(proxies)
		}
		if len(proxies) == 0 {
			fmt.Println("[PIAB] no proxies to verify")
			return
		}

		fmt.Printf("[PIAB] verifying proxies with %d workers ...\n", workers)
		type verifyResult struct {
			proxy   proxyinabox.Proxy
			country string
			delay   int64
		}
		var (
			verified []verifyResult
			mu       sync.Mutex
			wg       sync.WaitGroup
		)
		jobCh := make(chan proxyinabox.Proxy, workers)

		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for p := range jobCh {
					country, delay, err := crawler.ValidateProxy(p)
					if err == nil {
						mu.Lock()
						verified = append(verified, verifyResult{proxy: p, country: country, delay: delay})
						mu.Unlock()
					}
				}
			}()
		}

		for _, p := range proxies {
			jobCh <- p
		}
		close(jobCh)
		wg.Wait()

		fmt.Printf("[PIAB] ✅ verification complete: %d/%d proxies available\n", len(verified), len(proxies))
		for i, v := range verified {
			fmt.Printf("  %3d. %s %s:%s [%s] delay=%ds\n", i+1, v.proxy.Protocol, v.proxy.IP, v.proxy.Port, v.country, v.delay)
		}
	},
}

var rootCmd = &cobra.Command{
	Use:   "proxy-in-a-box",
	Short: "Proxy-in-a-Box provide many proxies.",
	Long:  `Proxy-in-a-Box helps programmers quickly and easily develop powerful crawler services. one-script, easy-to-use: proxies in a box.`,
	Run: func(cmd *cobra.Command, args []string) {
		proxyinabox.Init(configFilePath)
		fmt.Println("[PIAB]", "main", "[😁]", proxyinabox.Config.Sys.Name, version)
		proxyinabox.CI = service.NewMemCache()

		crawler.Init()

		m = newMITM()
		m.Init()

		m.ServeHTTP()

		// 加载 YAML 驱动的 proxy 源并启动抓取
		sources, err := crawler.LoadSources("./data/sources")
		if err != nil {
			fmt.Println("[PIAB]", "panic", "[👻]", err)
			os.Exit(1)
		}
		crawler.FetchAllSources(sources)
		crawler.Verify()
		crawler.CleanupStaleProxies()

		c := cron.New(cron.WithSeconds())
		// BUG-FIX: Verify() 只是轻量地查询过期代理并投递到 worker channel，高频调度不会造成资源压力。
		// 每 5 分钟拉取一次，确保过期代理能及时被重新验证
		c.AddFunc("@every 5m", crawler.Verify)
		// 每天凌晨清理超过 6 个月未验证的陈旧代理记录
		c.AddFunc("0 0 3 * * *", crawler.CleanupStaleProxies)
		c.Start()

		// 信号处理：统一的清理路径，确保 lightpanda 子进程被完整回收
		// os.Exit 不会触发 defer，所以必须在信号处理中显式调用清理函数
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			fmt.Printf("[PIAB] received signal %v, shutting down...\n", sig)
			c.Stop()
			crawler.StopLightpanda()
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
			var blockedIPCount int64
			proxyinabox.DB.Model(&proxyinabox.BlockedIP{}).Where("locked_until > ?", time.Now()).Count(&blockedIPCount)
			stats := map[string]interface{}{
				"version":     version,
				"total":       len(proxies),
				"by_protocol": byProtocol,
				"by_source":   bySource,
				"blocked_ips": blockedIPCount,
				"processes":   mitm.GetProcessCounts(),
				"request_stats": map[string]interface{}{
					"total":       mitm.GlobalRequestStats.Snapshot(),
					"by_upstream": mitm.GlobalUpstreamStats.Snapshot(),
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(stats)
		})

		// API: 全量代理列表
		managerHttpServer.HandleFunc("/api/proxies", func(w http.ResponseWriter, r *http.Request) {
			proxies := proxyinabox.CI.GetAllProxies()

			var blockedIPs []proxyinabox.BlockedIP
			proxyinabox.DB.Find(&blockedIPs)
			failuresByIP := make(map[string]int, len(blockedIPs))
			for _, b := range blockedIPs {
				failuresByIP[b.IP] = b.ConsecutiveFailures
			}

			type proxyJSON struct {
				IP                  string `json:"ip"`
				Port                string `json:"port"`
				Protocol            string `json:"protocol"`
				Country             string `json:"country"`
				Source              string `json:"source"`
				Delay               int64  `json:"delay"`
				LastVerify          string `json:"last_verify"`
				ConsecutiveFailures int    `json:"consecutive_failures"`
			}
			result := make([]proxyJSON, len(proxies))
			for i, p := range proxies {
				result[i] = proxyJSON{
					IP:                  p.IP,
					Port:                p.Port,
					Protocol:            p.Protocol,
					Country:             p.Country,
					Source:              p.Source,
					Delay:               p.Delay,
					LastVerify:          p.LastVerify.Format("2006-01-02T15:04:05Z"),
					ConsecutiveFailures: failuresByIP[p.IP],
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
		})

		// API: 各源抓取状态
		managerHttpServer.HandleFunc("/api/sources", func(w http.ResponseWriter, r *http.Request) {
			crawler.UpdateSourceAvailableCounts(proxyinabox.CI.GetAllProxies())
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
	testSourceCmd.Flags().IntVarP(&testSourceVerifyWorkers, "workers", "w", 20, "concurrent verification workers")
	rootCmd.AddCommand(testSourceCmd)
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
		OnProxyFailure: func(proxyURI string) {
			u, err := url.Parse(proxyURI)
			if err != nil || u.Hostname() == "" {
				return
			}
			proxyinabox.CI.RecordFailure(u.Hostname())
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
