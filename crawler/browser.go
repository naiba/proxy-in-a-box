package crawler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/naiba/proxyinabox"
)

// BrowserSession 管理单次浏览器抓取的完整生命周期（pinchtab 进程 + Chrome profile）
// 每个 runScript 调用创建独立 session，用完即销毁，避免资源泄漏和 cookie/指纹污染
type BrowserSession struct {
	cmd     *exec.Cmd
	port    string
	profile string
	client  *http.Client
	mu      sync.Mutex
}

// 当前活跃 session（每个 runScript goroutine 通过 goroutine-local 模式使用）
var (
	activeSession   *BrowserSession
	activeSessionMu sync.Mutex
)

func allocateRandomPort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return port, nil
}

func (s *BrowserSession) endpoint() string {
	return "http://127.0.0.1:" + s.port
}

func (s *BrowserSession) start() error {
	cfg := proxyinabox.Config.Pinchtab
	if cfg.Bin == "" {
		return fmt.Errorf("pinchtab not configured (set pinchtab.bin in config)")
	}

	port, err := allocateRandomPort()
	if err != nil {
		return fmt.Errorf("allocate port: %w", err)
	}
	s.port = port
	s.client = &http.Client{Timeout: 60 * time.Second}

	profileDir, err := os.MkdirTemp("", "piab-profile-*")
	if err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}
	s.profile = profileDir

	s.cmd = exec.Command(cfg.Bin)
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	s.cmd.Env = append(os.Environ(),
		"PINCHTAB_PORT="+s.port,
		"PINCHTAB_BIND=127.0.0.1",
		"PINCHTAB_HEADLESS=true",
		"PINCHTAB_ALLOW_EVALUATE=true",
		"PINCHTAB_STEALTH=full",
		"CHROME_FLAGS=--no-sandbox --disable-dev-shm-usage",
	)
	s.cmd.Stdout = os.Stdout
	s.cmd.Stderr = os.Stderr

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start pinchtab: %w", err)
	}

	pgidFile := filepath.Join(profileDir, "pgid")
	os.WriteFile(pgidFile, []byte(fmt.Sprintf("%d", s.cmd.Process.Pid)), 0644)

	fmt.Printf("[PIAB] pinchtab [🚀] started (pid=%d, port=%s, profile=%s)\n", s.cmd.Process.Pid, s.port, s.profile)

	for i := 0; i < 30; i++ {
		resp, err := http.Get(s.endpoint() + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Println("[PIAB] pinchtab [✅] ready")
				return nil
			}
		}
		time.Sleep(time.Second)
	}

	s.stop()
	return fmt.Errorf("pinchtab failed to become ready within 30s")
}

func (s *BrowserSession) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	fmt.Printf("[PIAB] pinchtab [🛑] stopping (pid=%d, profile=%s)\n", s.cmd.Process.Pid, s.profile)

	// 通过 HTTP API 请求优雅关闭，让 pinchtab 自己回收 Chrome 子进程
	s.client.Post(s.endpoint()+"/shutdown", "application/json", nil)

	done := make(chan struct{})
	go func() {
		s.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("[PIAB] pinchtab [✅] stopped")
	case <-time.After(10 * time.Second):
		fmt.Println("[PIAB] pinchtab [⚠️] kill process group")
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
		s.cmd.Wait()
	}

	s.cmd = nil

	if s.profile != "" {
		os.RemoveAll(s.profile)
		s.profile = ""
	}
}

func (s *BrowserSession) navigate(targetURL string) error {
	body, _ := json.Marshal(map[string]string{"url": targetURL})
	resp, err := s.client.Post(s.endpoint()+"/navigate", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("navigate failed (HTTP %d): %s", resp.StatusCode, string(data))
	}
	return nil
}

func (s *BrowserSession) evaluate(expression string) (string, error) {
	body, _ := json.Marshal(map[string]string{"expression": expression})
	resp, err := s.client.Post(s.endpoint()+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("evaluate failed (HTTP %d): %s", resp.StatusCode, string(data))
	}
	var result struct {
		Result interface{} `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse evaluate response: %w", err)
	}
	return fmt.Sprintf("%v", result.Result), nil
}

// BrowserFetch 启动临时 pinchtab 实例 → 导航到 URL → 等待 JS 渲染 → 返回 HTML
func BrowserFetch(targetURL string) (string, error) {
	activeSessionMu.Lock()
	if activeSession == nil {
		activeSession = &BrowserSession{}
		if err := activeSession.start(); err != nil {
			activeSession = nil
			activeSessionMu.Unlock()
			return "", err
		}
	}
	session := activeSession
	activeSessionMu.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()

	if err := session.navigate(targetURL); err != nil {
		return "", fmt.Errorf("navigate to %s failed: %w", targetURL, err)
	}

	time.Sleep(5 * time.Second)

	html, err := session.evaluate("document.body.innerHTML")
	if err != nil {
		return "", fmt.Errorf("failed to get rendered HTML: %w", err)
	}

	return html, nil
}

// BrowserEval 在当前 session 的页面上执行 JS 表达式
func BrowserEval(expression string) (string, error) {
	activeSessionMu.Lock()
	session := activeSession
	activeSessionMu.Unlock()

	if session == nil {
		return "", fmt.Errorf("no active browser session (call browser_fetch first)")
	}

	session.mu.Lock()
	defer session.mu.Unlock()

	return session.evaluate(expression)
}

// ReleaseBrowser 停止当前 pinchtab 实例并清理 Chrome profile，释放所有资源
func ReleaseBrowser() {
	activeSessionMu.Lock()
	session := activeSession
	activeSession = nil
	activeSessionMu.Unlock()

	if session == nil {
		return
	}

	session.mu.Lock()
	session.navigate("about:blank")
	session.mu.Unlock()

	session.stop()
}

// StartPinchtab 兼容 test-source 子命令的预启动接口
func StartPinchtab() error {
	activeSessionMu.Lock()
	defer activeSessionMu.Unlock()

	if activeSession != nil {
		return fmt.Errorf("pinchtab already running")
	}
	activeSession = &BrowserSession{}
	return activeSession.start()
}

// StopPinchtab 兼容 test-source 子命令和信号处理的停止接口
func StopPinchtab() {
	ReleaseBrowser()
}

// CleanupStaleSessions 清理上次异常退出遗留的 pinchtab 进程组
// 通过扫描 /tmp/piab-profile-*/pgid 找到残留的 PGID，kill 整个进程组后删除临时目录
func CleanupStaleSessions() {
	matches, err := filepath.Glob("/tmp/piab-profile-*/pgid")
	if err != nil || len(matches) == 0 {
		return
	}
	for _, pgidFile := range matches {
		data, err := os.ReadFile(pgidFile)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			continue
		}
		syscall.Kill(-pid, syscall.SIGKILL)
		profileDir := filepath.Dir(pgidFile)
		os.RemoveAll(profileDir)
		fmt.Printf("[PIAB] pinchtab [🧹] cleaned stale session (pgid=%d, profile=%s)\n", pid, profileDir)
	}
}
