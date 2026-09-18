package services

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
)

var (
	appleClientMu  sync.RWMutex
	appleHTTP      *req.Client
	customProxyURL string
)

// SetCustomProxy 设置并应用用户自定义网络代理 (支持 http:// 或 socks5://)。
func SetCustomProxy(rawProxy string) {
	rawProxy = strings.TrimSpace(rawProxy)
	appleClientMu.Lock()
	if customProxyURL == rawProxy {
		appleClientMu.Unlock()
		return
	}
	customProxyURL = rawProxy
	if appleHTTP != nil {
		if hc := appleHTTP.GetClient(); hc != nil {
			hc.CloseIdleConnections()
		}
	}
	appleHTTP = newAppleClient(customProxyURL)
	appleClientMu.Unlock()
}

// GetCustomProxy 获取当前配置的代理地址。
func GetCustomProxy() string {
	appleClientMu.RLock()
	defer appleClientMu.RUnlock()
	return customProxyURL
}

func newAppleClient(proxy string) *req.Client {
	jar, _ := cookiejar.New(nil)
	c := req.C().
		ImpersonateChrome().
		SetCookieJar(jar).
		SetTimeout(15 * time.Second)

	if proxy != "" {
		c.SetProxyURL(proxy)
	} else {
		c.SetProxy(http.ProxyFromEnvironment)
	}

	return c
}

func shopHTTP() *req.Client {
	appleClientMu.RLock()
	c := appleHTTP
	appleClientMu.RUnlock()
	if c != nil {
		return c
	}

	appleClientMu.Lock()
	defer appleClientMu.Unlock()
	if appleHTTP != nil {
		return appleHTTP
	}
	appleHTTP = newAppleClient(customProxyURL)
	return appleHTTP
}

// warmupShopPage 保持为空操作。
func warmupShopPage(pageURL, acceptLanguage string) {
}

// resetShopSession 重置 HTTP 客户端并强制断开所有已建立的连接与旧会话。
func resetShopSession() {
	appleClientMu.Lock()
	defer appleClientMu.Unlock()
	if appleHTTP != nil {
		if hc := appleHTTP.GetClient(); hc != nil {
			hc.CloseIdleConnections()
		}
	}
	appleHTTP = newAppleClient(customProxyURL)
}

// TestProxyConnection 测试指定代理的连通性与可用性。
func TestProxyConnection(proxy string) error {
	proxy = strings.TrimSpace(proxy)
	client := req.C().
		ImpersonateChrome().
		SetTimeout(10 * time.Second)

	if proxy != "" {
		client.SetProxyURL(proxy)
	} else {
		client.SetProxy(http.ProxyFromEnvironment)
	}

	resp, err := client.R().
		SetHeader("accept", "*/*").
		SetHeader("accept-language", "zh-CN,zh-Hans;q=0.9").
		SetHeader("referer", "https://www.apple.com.cn/shop/buy-iphone").
		Get("https://www.apple.com.cn/shop/retail/pickup-message?pl=true&parts.0=MJY94CH/A&location=100000")

	if err != nil {
		return fmt.Errorf("代理连接失败: %w", err)
	}
	if resp.GetStatusCode() == 541 {
		return fmt.Errorf("该代理当前已被 Apple/CDN 封禁(541)，请更换其他节点")
	}
	if resp.GetStatusCode() != 200 {
		return fmt.Errorf("HTTP 状态码异常: %d", resp.GetStatusCode())
	}
	return nil
}

