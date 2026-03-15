package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	lua "github.com/yuin/gopher-lua"

	"github.com/naiba/proxyinabox"
)

// runScript executes a Lua script from a Source using gopher-lua.
// Injects fetch(url, headers?), sleep(ms), json_decode(str), json_encode(table).
// Script must return a table of {ip, port, protocol}.
// 脚本执行有 300 秒超时限制，browser 操作较慢需要更长时间。
func runScript(src Source) ([]proxyinabox.Proxy, error) {
	L := lua.NewState()
	defer L.Close()
	defer ReleaseBrowser()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	L.SetContext(ctx)

	// inject fetch(url [, headers_table]) — calls GetDocFromURL internally
	L.SetGlobal("fetch", L.NewFunction(func(L *lua.LState) int {
		fetchURL := L.CheckString(1)

		var headers []http.Header
		// 合并 source 级别 headers 和脚本传入的 headers
		if len(src.Headers) > 0 {
			headers = append(headers, src.httpHeaders())
		}
		if L.GetTop() >= 2 {
			tbl, ok := L.Get(2).(*lua.LTable)
			if ok {
				h := make(http.Header)
				tbl.ForEach(func(key, val lua.LValue) {
					h.Set(key.String(), val.String())
				})
				headers = append(headers, h)
			}
		}

		body, err := GetDocFromURL(fetchURL, headers...)
		if err != nil {
			if proxyinabox.Config.Debug {
				fmt.Printf("[PIAB] %s [❎] script fetch error: %v\n", src.Name, err)
			}
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(body))
		return 1
	}))

	// inject sleep(ms)
	L.SetGlobal("sleep", L.NewFunction(func(L *lua.LState) int {
		ms := L.CheckInt64(1)
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return 0
	}))

	// inject json_decode(str) — parses JSON string into Lua table
	L.SetGlobal("json_decode", L.NewFunction(func(L *lua.LState) int {
		str := L.CheckString(1)
		var raw interface{}
		if err := json.Unmarshal([]byte(str), &raw); err != nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(goToLuaValue(L, raw))
		return 1
	}))

	// inject json_encode(table) — converts Lua table to JSON string
	L.SetGlobal("json_encode", L.NewFunction(func(L *lua.LState) int {
		val := L.Get(1)
		goVal := luaToGoValue(val)
		data, err := json.Marshal(goVal)
		if err != nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(string(data)))
		return 1
	}))
	// inject browser_fetch(url) — fetches a URL using lightpanda headless browser
	// Returns rendered HTML string or nil on error
	// 使用无头浏览器获取 JS 渲染后的 HTML，比 fetch 慢但能处理 JS 渲染的页面
	L.SetGlobal("browser_fetch", L.NewFunction(func(L *lua.LState) int {
		fetchURL := L.CheckString(1)
		html, err := BrowserFetch(fetchURL)
		if err != nil {
			if proxyinabox.Config.Debug {
				fmt.Printf("[PIAB] %s [❎] browser_fetch error: %v\n", src.Name, err)
			}
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(html))
		return 1
	}))

	// inject browser_eval(expression) — evaluates JS in current browser page
	// 必须先通过 browser_fetch 导航到页面，再用 browser_eval 执行 JS 表达式
	L.SetGlobal("browser_eval", L.NewFunction(func(L *lua.LState) int {
		expression := L.CheckString(1)
		result, err := BrowserEval(expression)
		if err != nil {
			if proxyinabox.Config.Debug {
				fmt.Printf("[PIAB] %s [\u274e] browser_eval error: %v\n", src.Name, err)
			}
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LString(result))
		return 1
	}))

	if err := L.DoString(src.Script); err != nil {
		return nil, fmt.Errorf("script execution error for %s: %w", src.Name, err)
	}

	// 导出结果: 期望脚本 return 一个 table [{ip, port, protocol}, ...]
	retVal := L.Get(-1)
	retTbl, ok := retVal.(*lua.LTable)
	if !ok {
		return nil, fmt.Errorf("script result for %s is not a table", src.Name)
	}

	var proxies []proxyinabox.Proxy
	retTbl.ForEach(func(_, item lua.LValue) {
		sub, ok := item.(*lua.LTable)
		if !ok {
			return
		}
		ip := sub.RawGetString("ip").String()
		// port 可能是 LNumber 或 LString，统一转为字符串
		port := fmt.Sprintf("%v", sub.RawGetString("port"))
		protocol := sub.RawGetString("protocol").String()

		if ip == "" || port == "" {
			return
		}
		proxies = append(proxies, proxyinabox.Proxy{
			IP:       ip,
			Port:     port,
			Source:   src.Name,
			Protocol: protocol,
		})
	})

	return proxies, nil
}

// goToLuaValue recursively converts a Go value (from JSON unmarshal) to a lua.LValue
func goToLuaValue(L *lua.LState, value interface{}) lua.LValue {
	switch v := value.(type) {
	case map[string]interface{}:
		tbl := L.NewTable()
		for k, val := range v {
			tbl.RawSetString(k, goToLuaValue(L, val))
		}
		return tbl
	case []interface{}:
		tbl := L.NewTable()
		for _, val := range v {
			tbl.Append(goToLuaValue(L, val))
		}
		return tbl
	case string:
		return lua.LString(v)
	case float64:
		return lua.LNumber(v)
	case bool:
		if v {
			return lua.LTrue
		}
		return lua.LFalse
	case nil:
		return lua.LNil
	default:
		return lua.LString(fmt.Sprintf("%v", v))
	}
}

// luaToGoValue recursively converts a lua.LValue to a Go value (for json_encode)
func luaToGoValue(value lua.LValue) interface{} {
	switch v := value.(type) {
	case *lua.LTable:
		// 判断是数组（连续整数 key 从 1 开始）还是 map
		maxn := v.MaxN()
		if maxn > 0 {
			arr := make([]interface{}, 0, maxn)
			for i := 1; i <= maxn; i++ {
				arr = append(arr, luaToGoValue(v.RawGetInt(i)))
			}
			return arr
		}
		m := make(map[string]interface{})
		v.ForEach(func(key, val lua.LValue) {
			if str, ok := key.(lua.LString); ok {
				m[string(str)] = luaToGoValue(val)
			}
		})
		return m
	case lua.LBool:
		return bool(v)
	case lua.LNumber:
		return float64(v)
	case lua.LString:
		return string(v)
	default:
		return nil
	}
}
