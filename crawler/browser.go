package crawler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/naiba/proxyinabox"
)

var (
	browserClient = &http.Client{Timeout: 60 * time.Second}
	pinchtabCmd   *exec.Cmd
	pinchtabMu    sync.Mutex
	// pinchtab 进程管理端口，由配置决定
	pinchtabPort string
	// browserOpMu 序列化所有浏览器操作，因为 pinchtab 单实例模式只有一个 Chrome 标签页
	browserOpMu sync.Mutex
)

// StartPinchtab 启动 pinchtab 子进程，等待其就绪
// 由 main.go 在启动时调用，仅当配置了 pinchtab.bin 时生效
func StartPinchtab() error {
	cfg := proxyinabox.Config.Pinchtab
	if cfg.Bin == "" {
		return nil
	}

	pinchtabMu.Lock()
	defer pinchtabMu.Unlock()

	if pinchtabCmd != nil {
		return fmt.Errorf("pinchtab already running")
	}

	pinchtabPort = cfg.Port
	if pinchtabPort == "" {
		pinchtabPort = "9867"
	}

	pinchtabCmd = exec.Command(cfg.Bin)
	// 使用进程组启动，确保 SIGTERM/SIGKILL 能杀掉 pinchtab 及其 Chrome 子进程树
	pinchtabCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	pinchtabCmd.Env = append(os.Environ(),
		"BRIDGE_PORT="+pinchtabPort,
		"BRIDGE_BIND=127.0.0.1",
		"BRIDGE_HEADLESS=true",
	)
	pinchtabCmd.Stdout = os.Stdout
	pinchtabCmd.Stderr = os.Stderr

	if err := pinchtabCmd.Start(); err != nil {
		pinchtabCmd = nil
		return fmt.Errorf("failed to start pinchtab: %w", err)
	}

	fmt.Printf("[PIAB] pinchtab [\U0001f680] started (pid=%d, port=%s)\n", pinchtabCmd.Process.Pid, pinchtabPort)

	// 等待 pinchtab 就绪（最多 30 秒）
	endpoint := pinchtabEndpoint()
	for i := 0; i < 30; i++ {
		resp, err := http.Get(endpoint + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				fmt.Println("[PIAB] pinchtab [\u2705] ready")
				return nil
			}
		}
		time.Sleep(time.Second)
	}

	// 超时，杀掉进程
	StopPinchtab()
	return fmt.Errorf("pinchtab failed to become ready within 30s")
}

// StopPinchtab 优雅停止 pinchtab 子进程及其 Chrome 子进程树
// 流程：SIGTERM → 等待 5 秒 → SIGKILL 整个进程组（兜底，防止 Chrome 残留）
func StopPinchtab() {
	pinchtabMu.Lock()
	defer pinchtabMu.Unlock()

	if pinchtabCmd == nil || pinchtabCmd.Process == nil {
		return
	}

	fmt.Println("[PIAB] pinchtab [\U0001f6d1] stopping...")

	// 1. 先发 SIGTERM，让 pinchtab 优雅退出并回收 Chrome
	syscall.Kill(-pinchtabCmd.Process.Pid, syscall.SIGTERM)

	// 2. 等待进程退出，最多 5 秒
	done := make(chan struct{})
	go func() {
		pinchtabCmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		fmt.Println("[PIAB] pinchtab [\u2705] stopped gracefully")
	case <-time.After(5 * time.Second):
		// 3. 超时兜底：SIGKILL 整个进程组，确保 Chrome 不残留
		fmt.Println("[PIAB] pinchtab [\u26a0\ufe0f] graceful stop timed out, killing process group")
		syscall.Kill(-pinchtabCmd.Process.Pid, syscall.SIGKILL)
		pinchtabCmd.Wait()
	}

	pinchtabCmd = nil
}

func pinchtabEndpoint() string {
	return "http://127.0.0.1:" + pinchtabPort
}

// browserAvailable 检查 pinchtab 是否可用
func browserAvailable() bool {
	return proxyinabox.Config.Pinchtab.Bin != "" && pinchtabCmd != nil
}

// BrowserFetch 使用无头浏览器获取 JS 渲染后的页面 HTML
// pinchtab 单实例模式：POST /navigate 导航 → 等待渲染 → POST /evaluate 提取 HTML
// 操作通过 browserOpMu 序列化，因为单实例只有一个标签页
func BrowserFetch(targetURL string) (string, error) {
	if !browserAvailable() {
		return "", fmt.Errorf("pinchtab not running (configure pinchtab.bin in config)")
	}

	browserOpMu.Lock()
	defer browserOpMu.Unlock()

	// 1. 导航到目标页面
	if err := navigate(targetURL); err != nil {
		return "", fmt.Errorf("navigate to %s failed: %w", targetURL, err)
	}

	// 2. 等待 JS 渲染完成
	time.Sleep(5 * time.Second)

	// 3. 提取渲染后的 HTML
	html, err := evaluate("document.body.innerHTML")
	if err != nil {
		return "", fmt.Errorf("failed to get rendered HTML: %w", err)
	}

	return html, nil
}

// BrowserEval 在当前页面执行 JavaScript 表达式并返回结果
// 注意：必须先通过 BrowserFetch 或 navigate 导航到页面
func BrowserEval(expression string) (string, error) {
	if !browserAvailable() {
		return "", fmt.Errorf("pinchtab not running")
	}

	browserOpMu.Lock()
	defer browserOpMu.Unlock()

	return evaluate(expression)
}

// navigate 调用 pinchtab 单实例 API 导航到 URL
func navigate(targetURL string) error {
	body, _ := json.Marshal(map[string]string{"url": targetURL})
	resp, err := browserClient.Post(pinchtabEndpoint()+"/navigate", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("navigate failed (HTTP %d): %s", resp.StatusCode, string(data))
	}
	return nil
}

// evaluate 调用 pinchtab 单实例 API 执行 JS 表达式
func evaluate(expression string) (string, error) {
	body, _ := json.Marshal(map[string]string{"expression": expression})
	resp, err := browserClient.Post(pinchtabEndpoint()+"/evaluate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("evaluate failed (HTTP %d): %s", resp.StatusCode, string(data))
	}
	var result struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	return result.Result, nil
}