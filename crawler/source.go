package crawler

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/naiba/proxyinabox"
	"gopkg.in/yaml.v3"
)

// Source represents a YAML-driven proxy source configuration
type Source struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"` // text, json, script
	URL           string            `yaml:"url"`
	Protocol      string            `yaml:"protocol"`
	Headers       map[string]string `yaml:"headers"`
	Interval      string            `yaml:"interval"`
	IPField       string            `yaml:"ip_field"`
	PortField     string            `yaml:"port_field"`
	ProtocolField string            `yaml:"protocol_field"`
	Script        string            `yaml:"script"`
}

// SourceStatus 记录每个 proxy 源的最近抓取状态，用于 dashboard 展示
type SourceStatus struct {
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	LastFetch  time.Time `json:"last_fetch"`
	ProxyCount int       `json:"proxy_count"`
	Error      string    `json:"error"`
	Interval   string    `json:"interval"`
	// AvailableCount 该源当前在代理池中验证通过的可用代理数（实时从缓存统计）
	AvailableCount int `json:"available_count"`
}

var (
	sourceStatuses   []SourceStatus
	sourceStatusesMu sync.RWMutex
)

// GetSourceStatuses 返回所有源状态的快照副本（线程安全）
func GetSourceStatuses() []SourceStatus {
	sourceStatusesMu.RLock()
	defer sourceStatusesMu.RUnlock()
	copy := make([]SourceStatus, len(sourceStatuses))
	for i, s := range sourceStatuses {
		copy[i] = s
	}
	return copy
}

func (s Source) intervalDuration() time.Duration {
	if s.Interval == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(s.Interval)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

func (s Source) httpHeaders() http.Header {
	h := make(http.Header)
	for k, v := range s.Headers {
		h.Set(k, v)
	}
	return h
}

// LoadSources reads all .yaml files from the given directory and returns parsed sources
func LoadSources(dir string) ([]Source, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read sources directory %s: %w", dir, err)
	}
	var sources []Source
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			fmt.Printf("[PIAB] source [❎] failed to read %s: %v\n", entry.Name(), err)
			continue
		}
		var src Source
		if err := yaml.Unmarshal(data, &src); err != nil {
			fmt.Printf("[PIAB] source [❎] failed to parse %s: %v\n", entry.Name(), err)
			continue
		}
		if src.Name == "" {
			// 使用文件名（去掉扩展名）作为 source name 的回退
			src.Name = strings.TrimSuffix(entry.Name(), ".yaml")
		}
		sources = append(sources, src)
	}
	return sources, nil
}

// FetchAllSources starts a goroutine per source to continuously fetch proxies
func FetchAllSources(sources []Source) {
	// 初始化源状态注册表
	sourceStatusesMu.Lock()
	sourceStatuses = make([]SourceStatus, len(sources))
	for i, src := range sources {
		sourceStatuses[i] = SourceStatus{
			Name:     src.Name,
			Type:     src.Type,
			Interval: src.Interval,
		}
	}
	sourceStatusesMu.Unlock()

	for i, src := range sources {
		go fetchSource(src, i)
	}
}

// TestFetchSource performs a single fetch for testing purposes (does not send to ValidateJobs)
func TestFetchSource(src Source) ([]proxyinabox.Proxy, error) {
	switch src.Type {
	case "text":
		return fetchTextSource(src)
	case "json":
		return fetchJSONSource(src)
	case "script":
		return runScript(src)
	default:
		return nil, fmt.Errorf("unknown source type: %s", src.Type)
	}
}

// updateSourceStatus 更新指定索引的源状态（每次抓取完成后调用）
func updateSourceStatus(index int, proxyCount int, fetchErr error) {
	sourceStatusesMu.Lock()
	defer sourceStatusesMu.Unlock()
	if index < 0 || index >= len(sourceStatuses) {
		return
	}
	sourceStatuses[index].LastFetch = time.Now()
	sourceStatuses[index].ProxyCount = proxyCount
	if fetchErr != nil {
		sourceStatuses[index].Error = fetchErr.Error()
	} else {
		sourceStatuses[index].Error = ""
	}
}

// UpdateSourceAvailableCounts 根据代理池快照更新各源的可用代理计数
func UpdateSourceAvailableCounts(proxies []proxyinabox.Proxy) {
	countBySource := make(map[string]int)
	for _, p := range proxies {
		countBySource[p.Source]++
	}
	sourceStatusesMu.Lock()
	defer sourceStatusesMu.Unlock()
	for i := range sourceStatuses {
		sourceStatuses[i].AvailableCount = countBySource[sourceStatuses[i].Name]
	}
}

func fetchSource(src Source, statusIndex int) {
	interval := src.intervalDuration()
	for {
		var proxies []proxyinabox.Proxy
		var err error

		switch src.Type {
		case "text":
			proxies, err = fetchTextSource(src)
		case "json":
			proxies, err = fetchJSONSource(src)
		case "script":
			proxies, err = runScript(src)
		default:
			fmt.Printf("[PIAB] %s [❎] unknown source type: %s\n", src.Name, src.Type)
			updateSourceStatus(statusIndex, 0, fmt.Errorf("unknown source type: %s", src.Type))
			return
		}

		if err != nil {
			fmt.Printf("[PIAB] %s [❎] fetch error: %v\n", src.Name, err)
			updateSourceStatus(statusIndex, 0, err)
		} else {
			fmt.Printf("[PIAB] %s [✅] fetched %d proxies\n", src.Name, len(proxies))
			updateSourceStatus(statusIndex, len(proxies), nil)
			for _, p := range proxies {
				ValidateJobs <- p
			}
		}

		time.Sleep(interval)
	}
}

func fetchTextSource(src Source) ([]proxyinabox.Proxy, error) {
	var headers []http.Header
	if len(src.Headers) > 0 {
		headers = append(headers, src.httpHeaders())
	}
	body, err := GetDocFromURL(src.URL, headers...)
	if err != nil {
		return nil, err
	}
	return parseTextResponse(body, src), nil
}

func fetchJSONSource(src Source) ([]proxyinabox.Proxy, error) {
	var headers []http.Header
	if len(src.Headers) > 0 {
		headers = append(headers, src.httpHeaders())
	}
	body, err := GetDocFromURL(src.URL, headers...)
	if err != nil {
		return nil, err
	}
	return parseJSONResponse(body, src), nil
}

// parseTextResponse parses a plain text body where each line is ip:port
// Handles spys.me format "ip:port EXTRA" by splitting on space first
// 同时支持 "protocol://ip:port" 格式（如 trio666 源），自动提取协议并剥离前缀
func parseTextResponse(body string, src Source) []proxyinabox.Proxy {
	var proxies []proxyinabox.Proxy
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// spys.me 格式: "ip:port COUNTRY-ANONYMITY-SSL-GOOGLE", 只取 ip:port 部分
		if idx := strings.IndexByte(line, ' '); idx != -1 {
			line = line[:idx]
		}
		// 支持 "protocol://ip:port" 格式：提取协议并剥离 scheme 前缀
		protocol := src.Protocol
		if schemeEnd := strings.Index(line, "://"); schemeEnd != -1 {
			protocol = strings.ToLower(line[:schemeEnd])
			line = line[schemeEnd+3:]
		}
		host, port, err := net.SplitHostPort(line)
		if err != nil {
			continue
		}
		proxies = append(proxies, proxyinabox.Proxy{
			IP:       host,
			Port:     port,
			Source:   src.Name,
			Protocol: protocol,
		})
	}
	return proxies
}

// parseJSONResponse parses a JSON body using field path extraction
// Field paths like "proxies.*.ip" mean: access "proxies" (array), iterate elements, extract "ip"
// Root array paths like "*.host" iterate the root array and extract "host"
func parseJSONResponse(body string, src Source) []proxyinabox.Proxy {
	var raw interface{}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		fmt.Printf("[PIAB] %s [❎] JSON parse error: %v\n", src.Name, err)
		return nil
	}

	ipValues := extractFieldPath(raw, src.IPField)
	portValues := extractFieldPath(raw, src.PortField)
	protocolValues := extractFieldPath(raw, src.ProtocolField)

	count := len(ipValues)
	if len(portValues) < count {
		count = len(portValues)
	}

	var proxies []proxyinabox.Proxy
	for i := 0; i < count; i++ {
		ip := toString(ipValues[i])
		port := toString(portValues[i])
		protocol := src.Protocol
		if i < len(protocolValues) {
			if p := toString(protocolValues[i]); p != "" {
				protocol = p
			}
		}
		if ip == "" || port == "" {
			continue
		}
		proxies = append(proxies, proxyinabox.Proxy{
			IP:       ip,
			Port:     port,
			Source:   src.Name,
			Protocol: protocol,
		})
	}
	return proxies
}

// extractFieldPath extracts values from parsed JSON using dot-path notation
// e.g. "proxies.*.ip" -> access "proxies" array, iterate, extract "ip" from each
// e.g. "*.host" -> iterate root array, extract "host" from each element
func extractFieldPath(data interface{}, path string) []interface{} {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	return extractRecursive([]interface{}{data}, parts)
}

func extractRecursive(current []interface{}, parts []string) []interface{} {
	if len(parts) == 0 {
		return current
	}
	part := parts[0]
	rest := parts[1:]

	var next []interface{}
	for _, item := range current {
		if part == "*" {
			// 展开数组元素
			switch v := item.(type) {
			case []interface{}:
				next = append(next, v...)
			}
		} else {
			// 访问 map 的 key
			switch v := item.(type) {
			case map[string]interface{}:
				if val, ok := v[part]; ok {
					next = append(next, val)
				}
			}
		}
	}
	return extractRecursive(next, rest)
}

// toString converts JSON values (string, float64, etc.) to string
// JSON 中 port 可能是整数也可能是字符串，都需要转换为字符串
func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%v", val)
	case json.Number:
		return val.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprintf("%v", v)
	}
}
