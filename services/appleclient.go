package services

import (
	"net/http"
	"sync"
	"time"

	"github.com/imroc/req/v3"
)

var (
	appleClientMu sync.RWMutex
	appleHTTP     *req.Client
)

func newAppleClient() *req.Client {
	return req.C().
		ImpersonateChrome().
		SetCookieJar(nil).
		SetProxy(http.ProxyFromEnvironment).
		SetTimeout(15 * time.Second)
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
	appleHTTP = newAppleClient()
	return appleHTTP
}

// warmupShopPage 保持为空操作。
// 此前访问 /shop/buy-iphone 会获取 Akamai 追踪令牌 (shld_bt_m / dssid2)，
// 累积请求后会被 Akamai 标记为爬虫导致 541 风控且长时间无法解封。
// 库存查询接口 /shop/retail/pickup-message 本身是公开无状态的，不带 Cookie 查询更安全持久。
func warmupShopPage(pageURL, acceptLanguage string) {
}

// resetShopSession 重置 HTTP 客户端并强制断开所有已建立的连接与会话。
// 避免因复用被标记的 TCP 连接或边缘节点缓存而持续处于 541 拦截状态。
func resetShopSession() {
	appleClientMu.Lock()
	defer appleClientMu.Unlock()
	if appleHTTP != nil {
		if hc := appleHTTP.GetClient(); hc != nil {
			hc.CloseIdleConnections()
		}
	}
	appleHTTP = newAppleClient()
}
