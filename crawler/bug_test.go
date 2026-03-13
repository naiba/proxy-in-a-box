package crawler

import (
	"sync"
	"testing"
	"time"

	"github.com/naiba/proxyinabox"
)

// TestValidator_LoadOrStoreAtomic 验证 LoadOrStore 原子操作防止多个 validator 同时验证同一代理
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
