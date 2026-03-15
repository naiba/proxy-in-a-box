package crawler

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/naiba/proxyinabox"
)

const (
	// cdpConnectTimeout 是等待 Lightpanda CDP 服务就绪的最大时间
	cdpConnectTimeout = 30 * time.Second
	// pageNavigationTimeout 是单次页面导航+渲染的最大时间
	pageNavigationTimeout = 60 * time.Second
	// healthCheckInterval 是轮询 CDP /json/version 的间隔
	healthCheckInterval = 300 * time.Millisecond
	// shutdownGracePeriod 是发送 SIGTERM 后等待进程退出的宽限期，超时则 SIGKILL
	shutdownGracePeriod = 3 * time.Second
)

// BrowserSession 管理单次浏览器抓取的完整生命周期（lightpanda 进程 + CDP 连接）
// 每个 runScript 调用创建独立 session，用完即销毁，避免资源泄漏
type BrowserSession struct {
	cmd       *exec.Cmd
	port      int
	browser   *rod.Browser
	page      *rod.Page
	proxyAddr string
	mu        sync.Mutex
}

// 当前活跃 session（每个 runScript goroutine 通过 goroutine-local 模式使用）
var (
	activeSession   *BrowserSession
	activeSessionMu sync.Mutex
)

func allocateRandomPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}

func (s *BrowserSession) start() error {
	cfg := proxyinabox.Config.Lightpanda
	// 默认在 $PATH 中查找 lightpanda，Docker 镜像已将其安装到 /usr/local/bin/
	if cfg.Bin == "" {
		cfg.Bin = "lightpanda"
	}
	if _, err := exec.LookPath(cfg.Bin); err != nil {
		return fmt.Errorf("lightpanda binary not found: %w (set lightpanda.bin in config or install lightpanda)", err)
	}

	port, err := allocateRandomPort()
	if err != nil {
		return fmt.Errorf("allocate port: %w", err)
	}
	s.port = port

	args := []string{
		"serve",
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(port),
	}
	if s.proxyAddr != "" {
		args = append(args, "--http_proxy", s.proxyAddr)
	}

	s.cmd = exec.Command(cfg.Bin, args...)
	s.cmd.Stdout = os.Stdout
	s.cmd.Stderr = os.Stderr
	// Lightpanda 无需 Setpgid/Pdeathsig，SIGTERM 即可正常退出
	s.cmd.Env = append(os.Environ(),
		"LIGHTPANDA_DISABLE_TELEMETRY=true",
		// BUG-FIX: Lightpanda 通过 std.fs.getAppDataDir("lightpanda") 解析数据目录，
		// 该调用依赖 $XDG_DATA_HOME。非 root 用户（如 Docker 中 UID 65534）没有可写的
		// home 目录，导致默认路径 $HOME/.local/share/lightpanda 写入时报 AccessDenied。
		"XDG_DATA_HOME=/tmp",
	)

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start lightpanda: %w", err)
	}

	fmt.Printf("[PIAB] lightpanda [🚀] started (pid=%d, port=%d)\n", s.cmd.Process.Pid, s.port)

	// 等待 CDP 服务就绪：轮询 /json/version 获取 WebSocket URL
	wsURL, err := s.waitForCDP()
	if err != nil {
		s.stop()
		return err
	}

	// 通过 go-rod 连接 CDP WebSocket
	s.browser = rod.New().ControlURL(wsURL)
	if err := s.browser.Connect(); err != nil {
		s.stop()
		return fmt.Errorf("CDP connect failed: %w", err)
	}

	fmt.Println("[PIAB] lightpanda [✅] ready")
	return nil
}

func (s *BrowserSession) waitForCDP() (string, error) {
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	deadline := time.Now().Add(cdpConnectTimeout)

	for time.Now().Before(deadline) {
		// launcher.ResolveURL 查询 /json/version 并提取 WS URL
		wsURL, err := launcher.ResolveURL(addr)
		if err == nil && wsURL != "" {
			return wsURL, nil
		}
		time.Sleep(healthCheckInterval)
	}

	return "", fmt.Errorf("lightpanda CDP not ready on port %d after %s", s.port, cdpConnectTimeout)
}

func (s *BrowserSession) navigate(targetURL string) error {
	// 每次导航创建新的 page target，确保隔离
	if s.page != nil {
		_ = s.page.Close()
		s.page = nil
	}

	page, err := s.browser.Page(proto.TargetCreateTarget{URL: targetURL})
	if err != nil {
		return fmt.Errorf("create page failed: %w", err)
	}

	err = page.Timeout(pageNavigationTimeout).WaitLoad()
	if err != nil {
		_ = page.Close()
		return fmt.Errorf("wait load failed for %s: %w", targetURL, err)
	}

	s.page = page
	return nil
}

func (s *BrowserSession) evaluate(expression string) (string, error) {
	if s.page == nil {
		return "", fmt.Errorf("no page loaded (call navigate first)")
	}

	// go-rod Eval 需要传入 JS 函数形式
	result, err := s.page.Timeout(pageNavigationTimeout).Eval(`() => ` + expression)
	if err != nil {
		return "", fmt.Errorf("evaluate failed: %w", err)
	}
	return result.Value.Str(), nil
}

func (s *BrowserSession) stop() {
	if s.page != nil {
		_ = s.page.Close()
		s.page = nil
	}

	if s.browser != nil {
		s.browser.Close()
		s.browser = nil
	}

	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	fmt.Printf("[PIAB] lightpanda [🛑] stopping (pid=%d)\n", s.cmd.Process.Pid)

	// 优雅关闭：先 SIGTERM，超时则 SIGKILL
	_ = s.cmd.Process.Signal(os.Interrupt)

	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()

	select {
	case <-done:
		fmt.Println("[PIAB] lightpanda [✅] stopped")
	case <-time.After(shutdownGracePeriod):
		fmt.Println("[PIAB] lightpanda [⚠️] force killing")
		_ = s.cmd.Process.Kill()
		<-done
	}

	s.cmd = nil
}

// startSessionWithProxy 启动带指定 proxy 的 lightpanda 实例，设置为 activeSession
func startSessionWithProxy(proxyAddr string) (*BrowserSession, error) {
	session := &BrowserSession{proxyAddr: proxyAddr}
	if err := session.start(); err != nil {
		return nil, err
	}
	activeSession = session
	return session, nil
}

// destroyActiveSession 销毁当前 activeSession（调用方必须持有 activeSessionMu）
func destroyActiveSession() {
	if activeSession == nil {
		return
	}
	session := activeSession
	activeSession = nil
	session.stop()
}

// BrowserFetch 启动临时 lightpanda 实例 → 导航到 URL → 等待 JS 渲染 → 返回 HTML
// 优先通过代理池中的随机 proxy 启动浏览器（lightpanda --http_proxy），若导航失败则销毁 session 并用直连重试。
func BrowserFetch(targetURL string) (string, error) {
	activeSessionMu.Lock()

	if activeSession == nil {
		var proxyAddr string
		if proxyinabox.CI != nil {
			proxyAddr, _ = proxyinabox.CI.RandomProxy()
		}
		if _, err := startSessionWithProxy(proxyAddr); err != nil {
			activeSessionMu.Unlock()
			fmt.Printf("[PIAB] browser [❎] start failed (proxy=%q): %v\n", proxyAddr, err)
			return "", err
		}
	}
	session := activeSession
	activeSessionMu.Unlock()

	session.mu.Lock()
	err := session.navigate(targetURL)
	session.mu.Unlock()

	if err != nil && session.proxyAddr != "" {
		fmt.Printf("[PIAB] browser [⚠️] proxy navigate failed for %s, fallback to direct: %v\n", targetURL, err)
		activeSessionMu.Lock()
		destroyActiveSession()
		fallbackSession, startErr := startSessionWithProxy("")
		activeSessionMu.Unlock()
		if startErr != nil {
			fmt.Printf("[PIAB] browser [❎] fallback direct start failed: %v\n", startErr)
			return "", fmt.Errorf("fallback direct browser start failed: %w", startErr)
		}

		fallbackSession.mu.Lock()
		err = fallbackSession.navigate(targetURL)
		fallbackSession.mu.Unlock()
		if err != nil {
			fmt.Printf("[PIAB] browser [❎] navigate failed (direct fallback) for %s: %v\n", targetURL, err)
			return "", fmt.Errorf("navigate to %s failed (direct fallback): %w", targetURL, err)
		}
		session = fallbackSession
	} else if err != nil {
		fmt.Printf("[PIAB] browser [❎] navigate failed for %s: %v\n", targetURL, err)
		return "", fmt.Errorf("navigate to %s failed: %w", targetURL, err)
	}

	// lightpanda 加载完成后直接通过 CDP 获取 HTML，无需额外等待
	session.mu.Lock()
	html, err := session.evaluate("document.body.innerHTML")
	session.mu.Unlock()
	if err != nil {
		fmt.Printf("[PIAB] browser [❎] evaluate failed for %s: %v\n", targetURL, err)
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

// ReleaseBrowser 停止当前 lightpanda 实例，释放所有资源
func ReleaseBrowser() {
	activeSessionMu.Lock()
	destroyActiveSession()
	activeSessionMu.Unlock()
}

// StartLightpanda 兼容 test-source 子命令的预启动接口
func StartLightpanda() error {
	activeSessionMu.Lock()
	defer activeSessionMu.Unlock()

	if activeSession != nil {
		return fmt.Errorf("lightpanda already running")
	}
	activeSession = &BrowserSession{}
	return activeSession.start()
}

// StopLightpanda 兼容 test-source 子命令和信号处理的停止接口
func StopLightpanda() {
	ReleaseBrowser()
}
