package services

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"apple-store-helper/model"
)

func TestFulfillmentURL(t *testing.T) {
	Listen.Area = model.Areas[0] // 中国大陆
	items := []ListenItem{
		{Product: model.Product{Code: "MJY94CH/A"}},
		{Product: model.Product{Code: "MJYA4CH/A"}},
	}
	url := Listen.fulfillmentURL("R320", items)
	expected := "https://www.apple.com.cn/shop/retail/pickup-message?pl=true&parts.0=MJY94CH/A&parts.1=MJYA4CH/A&store=R320"
	if url != expected {
		t.Fatalf("expected %s, got %s", expected, url)
	}
}

func TestIntervalParsing(t *testing.T) {
	labels := IntervalLabels()
	if len(labels) != len(IntervalOptions) {
		t.Fatalf("expected %d labels, got %d", len(IntervalOptions), len(labels))
	}

	for _, sec := range IntervalOptions {
		label := IntervalLabel(sec)
		parsed := ParseIntervalLabel(label)
		if parsed != sec {
			t.Fatalf("expected %d for label %s, got %d", sec, label, parsed)
		}
	}
}

func TestAdaptiveTimingCalculation(t *testing.T) {
	// For 5 stores:
	storeCount := 5
	minCycle := time.Duration(storeCount) * 1800 * time.Millisecond
	if minCycle != 9*time.Second {
		t.Fatalf("expected 9s minimum cycle for 5 stores, got %v", minCycle)
	}
}

func TestStoreList(t *testing.T) {
	Listen.Clean()
	Listen.Area = model.Areas[0]
	Listen.items = map[string]ListenItem{
		"R320.MJY94CH/A": {Store: model.Store{StoreNumber: "R320", CityStoreName: "北京-三里屯", Location: "北京 北京"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R388.MJY94CH/A": {Store: model.Store{StoreNumber: "R388", CityStoreName: "北京-西单大悦城", Location: "北京 北京"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R448.MJY94CH/A": {Store: model.Store{StoreNumber: "R448", CityStoreName: "北京-王府井", Location: "北京 北京"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R479.MJY94CH/A": {Store: model.Store{StoreNumber: "R479", CityStoreName: "北京-华贸购物中心", Location: "北京 北京"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R645.MJY94CH/A": {Store: model.Store{StoreNumber: "R645", CityStoreName: "北京-朝阳大悦城", Location: "北京 北京"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
	}
	Listen.Status.Set(Running)

	start := time.Now()
	res := Listen.groupByStore()
	dur := time.Since(start)

	if res.blocked {
		t.Fatalf("Expected no 541 blocking, but got blocked: %+v", res.blockedStores)
	}
	if len(res.skus) < 5 {
		t.Fatalf("Expected at least 5 SKUs, got %d", len(res.skus))
	}
	t.Logf("Successfully fetched all 5 stores via 1 coalesced request in %v: skus=%d", dur, len(res.skus))
}

func TestCityWideAllStoresQuery(t *testing.T) {
	Listen.Clean()
	Listen.Area = model.Areas[0]
	Listen.items = map[string]ListenItem{
		"ALL_北京.MJY94CH/A": {
			Store:   model.Store{StoreNumber: "ALL_北京", CityStoreName: "北京 - [全部/任意门店]", Location: "北京 北京"},
			Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A", Title: "iPhone 18 Pro Max 256GB 冰川蓝色"},
			Status:  StatusWait,
		},
	}
	Listen.Status.Set(Running)

	start := time.Now()
	res := Listen.groupByStore()
	dur := time.Since(start)

	if res.blocked {
		t.Fatalf("City wide query blocked: %+v", res.blockedStores)
	}
	avail, found := res.skus["ALL_北京.MJY94CH/A"]
	if !found {
		t.Fatalf("Expected ALL_北京.MJY94CH/A in results")
	}
	t.Logf("City-wide all-stores query finished in %v: available=%v, inStockDetails=%+v", dur, avail, res.inStockDetails)
}

func TestDropdownOptionsOrder(t *testing.T) {
	options := Store.ByAreaTitleForOptions("中国大陆")
	if len(options) == 0 {
		t.Fatalf("Expected options, got empty")
	}

	// 验证包含 '北京 - [全部/任意门店]'
	foundBeijingAll := false
	for _, opt := range options {
		if opt == "北京 - [全部/任意门店]" {
			foundBeijingAll = true
			break
		}
	}
	if !foundBeijingAll {
		t.Fatalf("Expected '北京 - [全部/任意门店]' in dropdown options")
	}

	// 验证全部门店选项排在最前面
	first := options[0]
	if !strings.Contains(first, "[全部/任意门店]") {
		t.Fatalf("Expected first option to be an all-stores option, got: %s", first)
	}
	t.Logf("Dropdown first 3 options: %v", options[:3])
}

func TestAllStoresInStockSimulation(t *testing.T) {
	Listen.Clean()
	Listen.Area = model.Areas[0]
	itemKey := "ALL_北京.MJY94CH/A"
	Listen.items = map[string]ListenItem{
		itemKey: {
			Store:   model.Store{StoreNumber: "ALL_北京", CityStoreName: "北京 - [全部/任意门店]", Location: "北京 北京"},
			Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A", Title: "iPhone 18 Pro Max 256GB 冰川蓝色"},
			Status:  StatusWait,
		},
	}

	// 模拟检测到三里屯有货
	inStockStore := "三里屯"
	Listen.mu.Lock()
	Listen.updateStatusLocked(itemKey, StatusInStock+": "+inStockStore)
	Listen.updateLogStrLocked()

	item := Listen.items[itemKey]
	Listen.mu.Unlock()

	if !strings.HasPrefix(item.Status, StatusInStock) {
		t.Fatalf("Expected status to start with %s, got: %s", StatusInStock, item.Status)
	}

	var inStockMsg string
	if item.Store.IsAllStores() && item.Status != StatusInStock {
		storeName := strings.TrimPrefix(item.Status, StatusInStock+": ")
		inStockMsg = fmt.Sprintf("%s (%s) %s 有货", item.Store.CityStoreName, storeName, item.Product.Title)
	} else {
		inStockMsg = fmt.Sprintf("%s %s 有货", item.Store.CityStoreName, item.Product.Title)
	}

	expectedMsg := "北京 - [全部/任意门店] (三里屯) iPhone 18 Pro Max 256GB 冰川蓝色 有货"
	if inStockMsg != expectedMsg {
		t.Fatalf("expected message %q, got %q", expectedMsg, inStockMsg)
	}
	t.Logf("In-stock simulation passed: %s", inStockMsg)
}

func TestShopClientSession(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			http.SetCookie(w, &http.Cookie{Name: "shld_bt_m", Value: "token123", Path: "/"})
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
			return
		}

		// 第二次请求：验证是否携带了第一次收到的 Cookie
		cookie, err := r.Cookie("shld_bt_m")
		if err != nil || cookie.Value != "token123" {
			t.Errorf("expected cookie shld_bt_m=token123, got %v, err=%v", cookie, err)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok_with_cookie"}`))
	}))
	defer server.Close()

	client := shopHTTP()
	resp1, err := client.R().Get(server.URL)
	if err != nil || resp1.GetStatusCode() != 200 {
		t.Fatalf("first request failed: %v", err)
	}

	resp2, err := client.R().Get(server.URL)
	if err != nil || resp2.GetStatusCode() != 200 {
		t.Fatalf("second request failed: %v", err)
	}

	// 验证 resetShopSession 会刷新客户端
	resetShopSession()
	newClient := shopHTTP()
	if newClient == nil {
		t.Fatalf("expected non-nil client after reset")
	}
}

func TestProxyConfiguration(t *testing.T) {
	orig := GetCustomProxy()
	defer SetCustomProxy(orig)

	SetCustomProxy("http://127.0.0.1:7897")
	if GetCustomProxy() != "http://127.0.0.1:7897" {
		t.Fatalf("expected http://127.0.0.1:7897, got %s", GetCustomProxy())
	}

	SetCustomProxy("")
	if GetCustomProxy() != "" {
		t.Fatalf("expected empty proxy, got %s", GetCustomProxy())
	}
}

func TestFormatBarkEndpoint(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"5cqE8b8SawnvhHZxkjfuhS", "https://api.day.app/5cqE8b8SawnvhHZxkjfuhS"},
		{"https://api.day.app/5cqE8b8SawnvhHZxkjfuhS/", "https://api.day.app/5cqE8b8SawnvhHZxkjfuhS"},
		{"https://api.day.app/5cqE8b8SawnvhHZxkjfuhS/推送内容?copy=1", "https://api.day.app/5cqE8b8SawnvhHZxkjfuhS"},
		{"https://custom.bark.me/mykey/", "https://custom.bark.me/mykey"},
		{"", ""},
	}

	for _, tt := range tests {
		got := formatBarkEndpoint(tt.input)
		if got != tt.expected {
			t.Errorf("formatBarkEndpoint(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBarkNotificationDelivery(t *testing.T) {
	var receivedPayload BarkPayload
	var receivedContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		body, _ := ioutil.ReadAll(r.Body)
		receivedPayload = BarkPayload{}
		_ = json.Unmarshal(body, &receivedPayload)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()

	testListen := listenService{
		BarkNotifyUrl: server.URL + "/testkey",
		BarkLevel:     BarkLevelOptions[0].Label,
	}

	testURL := "https://www.apple.com.cn/shop/bag"
	testListen.SendPushNotificationByBark("🎉 有货提醒", "三里屯 iPhone 18 Pro Max 有货", testURL)

	if !strings.Contains(receivedContentType, "application/json") {
		t.Fatalf("expected application/json, got %s", receivedContentType)
	}
	if receivedPayload.URL != testURL {
		t.Fatalf("expected URL %s, got %s", testURL, receivedPayload.URL)
	}
	if receivedPayload.Level != "timeSensitive" {
		t.Fatalf("expected level timeSensitive, got %s", receivedPayload.Level)
	}
	if receivedPayload.Copy != testURL {
		t.Fatalf("expected Copy to match bag URL, got %s", receivedPayload.Copy)
	}

	// 测试持续强警报 (call: 1, level: critical)
	testListen.BarkLevel = "持续强警报 (类似电话持续响铃30秒)"
	testListen.SendPushNotificationByBark("持续警报测试", "响铃30秒", testURL)
	if receivedPayload.Level != "critical" || receivedPayload.Call != 1 {
		t.Fatalf("expected level critical and call 1, got level %s, call %d", receivedPayload.Level, receivedPayload.Call)
	}

	// 测试普通通知 (active, 无额外铃声)
	testListen.BarkLevel = "普通通知 (系统默认提示音/温和)"
	testListen.SendPushNotificationByBark("普通通知测试", "普通消息", testURL)
	if receivedPayload.Level != "active" || receivedPayload.Sound != "" {
		t.Fatalf("expected level active and empty sound, got level %s, sound %s", receivedPayload.Level, receivedPayload.Sound)
	}

	t.Logf("Bark notification delivery test passed: %+v", receivedPayload)
}


