package crawler

import (
	"sync"
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

// ==================== BUG-3: pendingValidate 非原子检查测试 ====================
// 位置: crawler/crawler.go:86-111
// 问题: Load 和 Store 不是原子操作，多个 validator 可能同时验证同一代理

func TestValidator_PendingValidateRace(t *testing.T) {

	// 创建多个相同的代理
	p := proxyinabox.Proxy{
		IP:       "1.1.1.1",
		Port:     "8080",
		Protocol: "http",
		Source:   "test",
	}

	// 初始化 pendingValidate
	pendingValidate = sync.Map{}

	// 创建验证 jobs channel
	jobs := make(chan proxyinabox.Proxy, 10)

	// 启动多个 validator goroutine
	const numValidators = 5
	var wg sync.WaitGroup
	wg.Add(numValidators)

	validatedCount := 0
	var mu sync.Mutex

	for i := 0; i < numValidators; i++ {
		go func(id int) {
			defer wg.Done()
			// 模拟 validator 逻辑的关键部分
			proxy := p.URI()
			_, has := pendingValidate.Load(proxy)
			if !has {
				// BUG: 这里和 Store 之间有时间窗口
				pendingValidate.Store(proxy, id)
				time.Sleep(10 * time.Millisecond) // 模拟验证耗时
				mu.Lock()
				validatedCount++
				mu.Unlock()
				pendingValidate.Delete(proxy)
			}
		}(i)
	}

	// 向 channel 投递同一代理多次
	for i := 0; i < numValidators; i++ {
		jobs <- p
	}
	close(jobs)

	wg.Wait()

	// 验证: 同一代理应该只被验证一次
	// BUG: 由于竞态，可能验证多次
	if validatedCount > 1 {
		t.Errorf("BUG CONFIRMED: Same proxy validated %d times, want 1 (race in pendingValidate)", validatedCount)
	}
}

// TestValidator_LoadOrStoreAtomic 验证使用 LoadOrStore 可以解决竞态
func TestValidator_LoadOrStoreAtomic(t *testing.T) {
	p := proxyinabox.Proxy{
		IP:       "2.2.2.2",
		Port:     "8080",
		Protocol: "http",
		Source:   "test",
	}

	pendingValidate = sync.Map{}

	const numValidators = 5
	var wg sync.WaitGroup
	wg.Add(numValidators)

	validatedCount := 0
	var mu sync.Mutex

	for i := 0; i < numValidators; i++ {
		go func(id int) {
			defer wg.Done()
			proxy := p.URI()
			// 使用 LoadOrStore 原子操作
			_, loaded := pendingValidate.LoadOrStore(proxy, id)
			if !loaded {
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				validatedCount++
				mu.Unlock()
				pendingValidate.Delete(proxy)
			}
		}(i)
	}

	// 等待所有 goroutine
	wg.Wait()

	// 使用 LoadOrStore 应该只验证一次
	if validatedCount != 1 {
		t.Errorf("With LoadOrStore: validated %d times, want 1", validatedCount)
	}
}
