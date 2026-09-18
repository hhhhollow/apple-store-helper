package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/faiface/beep"
	"github.com/faiface/beep/mp3"
	"github.com/faiface/beep/speaker"
	"github.com/golang-module/carbon"
	"github.com/tidwall/gjson"

	"apple-store-helper/model"
	"apple-store-helper/theme"
	"apple-store-helper/view"
)

const (
	StatusOutStock = "无货"
	StatusInStock  = "有货"
	StatusWait     = "等待"
	StatusBlocked  = "风控重试"

	Pause   = "暂停"
	Running = "监听中"

	DefaultIntervalSeconds = 10
	staggerDelay           = 1800 * time.Millisecond
	baseBlockedBackoff     = 210 * time.Second
	maxBlockedBackoff      = 420 * time.Second
	microBreakThreshold    = 25
	microBreakDuration     = 35 * time.Second
	warmupProbationRounds  = 3
	warmupInterval         = 35 * time.Second
)

// IntervalOptions 用户可选的轮询间隔档位(秒)。
var IntervalOptions = []int{10, 15, 8, 20, 30, 60}

// IntervalLabel 把秒数转成下拉框显示文案。
func IntervalLabel(seconds int) string {
	switch seconds {
	case 10:
		return "10 秒 (推荐默认 - 均衡低风控)"
	case 15:
		return "15 秒 (高稳健 - 极少风控)"
	case 8:
		return "8 秒 (快速档 - 易遇频控)"
	case 5:
		return "5 秒 (极速冲刺 - 短期高危)"
	case 20:
		return "20 秒 (低频稳妥)"
	case 30:
		return "30 秒 (长期挂机)"
	case 60:
		return "60 秒 (超低频挂机)"
	default:
		return fmt.Sprintf("%d 秒", seconds)
	}
}

// IntervalLabels 生成下拉框完整选项列表。
func IntervalLabels() []string {
	labels := make([]string, 0, len(IntervalOptions))
	for _, s := range IntervalOptions {
		labels = append(labels, IntervalLabel(s))
	}
	return labels
}

// ParseIntervalLabel 把下拉框选中的文案反解成秒数;无法识别时回退到默认值。
func ParseIntervalLabel(label string) int {
	for _, s := range IntervalOptions {
		if IntervalLabel(s) == label {
			return s
		}
	}
	return DefaultIntervalSeconds
}

type BarkLevelOption struct {
	Label string
	Level string
	Sound string
	Call  int
}

var BarkLevelOptions = []BarkLevelOption{
	{
		Label: "时效性通知 (锁屏置顶/穿透勿扰/警报音)",
		Level: "timeSensitive",
		Sound: "alarm",
		Call:  0,
	},
	{
		Label: "紧急重要警告 (静音也强制响铃)",
		Level: "critical",
		Sound: "alarm",
		Call:  0,
	},
	{
		Label: "持续强警报 (类似电话持续响铃30秒)",
		Level: "critical",
		Sound: "alarm",
		Call:  1,
	},
	{
		Label: "普通通知 (系统默认提示音/温和)",
		Level: "active",
		Sound: "",
		Call:  0,
	},
}

func BarkLevelLabels() []string {
	labels := make([]string, 0, len(BarkLevelOptions))
	for _, opt := range BarkLevelOptions {
		labels = append(labels, opt.Label)
	}
	return labels
}

func GetBarkLevelOption(labelOrKey string) BarkLevelOption {
	for _, opt := range BarkLevelOptions {
		if opt.Label == labelOrKey || opt.Level == labelOrKey {
			return opt
		}
	}
	return BarkLevelOptions[0]
}

var Listen = listenService{
	items:           map[string]ListenItem{},
	Status:          binding.NewString(),
	Area:            model.Areas[0],
	Logs:            widget.NewLabel(""),
	BarkLevel:       BarkLevelOptions[0].Label,
	IntervalSeconds: binding.NewInt(),
}

type listenService struct {
	mu              sync.RWMutex
	items           map[string]ListenItem
	Status          binding.String
	Area            model.Area
	Logs            *widget.Label
	BarkNotifyUrl   string
	BarkLevel       string
	IntervalSeconds binding.Int
}

type ListenItem struct {
	Store   model.Store
	Product model.Product
	Status  string
	Time    carbon.DateTime
}

func (s *listenService) IntervalDuration() time.Duration {
	seconds, err := s.IntervalSeconds.Get()
	if err != nil || seconds < 5 {
		seconds = DefaultIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (s *listenService) Add(areaTitle string, storeTitle string, productTitle string, barkNotifyUrl string, barkLevel ...string) {
	store := Store.GetStore(areaTitle, storeTitle)
	product := Product.GetProduct(areaTitle, productTitle)

	uniqKey := store.StoreNumber + "." + product.Code

	s.mu.Lock()
	if s.items[uniqKey].Store.StoreNumber == "" {
		s.items[uniqKey] = ListenItem{
			Store:   store,
			Product: product,
			Status:  StatusWait,
		}
	}
	s.BarkNotifyUrl = barkNotifyUrl
	if len(barkLevel) > 0 && barkLevel[0] != "" {
		s.BarkLevel = barkLevel[0]
	}
	s.updateLogStrLocked()
	s.mu.Unlock()
}

func (s *listenService) Clean() {
	s.mu.Lock()
	s.items = map[string]ListenItem{}
	s.updateLogStrLocked()
	s.mu.Unlock()
}

func (s *listenService) SetListenItems(items map[string]ListenItem) {
	s.mu.Lock()
	s.items = items
	s.updateLogStrLocked()
	s.mu.Unlock()
}

func (s *listenService) GetListenItems() map[string]ListenItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make(map[string]ListenItem, len(s.items))
	for k, v := range s.items {
		res[k] = v
	}
	return res
}

func (s *listenService) updateLogStrLocked() {
	var str string
	for _, item := range s.items {
		str += fmt.Sprintf(
			"[%s] %s %s %s\n",
			item.Status,
			item.Time,
			item.Store.CityStoreName,
			item.Product.Title,
		)
	}
	s.Logs.SetText(str)
}

func (s *listenService) UpdateLogStr() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.updateLogStrLocked()
}

func (s *listenService) updateStatusLocked(uniqKey string, status string) {
	item := s.items[uniqKey]
	item.Time = carbon.DateTime{Carbon: carbon.Now(carbon.Shanghai)}
	item.Status = status
	s.items[uniqKey] = item
}

func (s *listenService) UpdateStatus(uniqKey string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateStatusLocked(uniqKey, status)
}

func (s *listenService) Run() {
	s.Status.Set(Pause)

	go func() {
		const idleSleep = 200 * time.Millisecond
		var consecutiveBlocks int
		var continuousSuccessCount int
		var warmupRoundsLeft int

		for {
			stats, ok := s.Status.Get()
			running := ok == nil && stats == Running

			s.mu.RLock()
			itemCount := len(s.items)
			s.mu.RUnlock()

			if !running || itemCount == 0 {
				consecutiveBlocks = 0
				continuousSuccessCount = 0
				warmupRoundsLeft = 0
				time.Sleep(idleSleep)
				continue
			}

			// 主动防疲劳微休息：连续正常成功轮询达到阈值时，主动插入短暂休眠，避免触碰 Apple 源站滑动窗口配额
			if continuousSuccessCount >= microBreakThreshold {
				continuousSuccessCount = 0
				breakDur := microBreakDuration + time.Duration(rand.Intn(10))*time.Second
				log.Printf("[防风控保护] 已连续正常轮询 %d 次，启动主动防疲劳微休息 %v 规避 Apple/CDN 滑动窗口配额...\n", microBreakThreshold, breakDur)

				s.mu.Lock()
				statusLabel := fmt.Sprintf("微休息(%ds)", int(breakDur.Seconds()))
				for key, item := range s.items {
					if item.Status != StatusInStock {
						s.updateStatusLocked(key, statusLabel)
					}
				}
				s.updateLogStrLocked()
				s.mu.Unlock()

				time.Sleep(breakDur)

				// 检查休眠后是否仍处于运行状态
				statsAfter, okAfter := s.Status.Get()
				if okAfter != nil || statsAfter != Running {
					continue
				}
			}

			result := s.groupByStore()

			s.mu.Lock()
			hasInStock := false
			var inStockBagURL string
			var inStockMsg string

			for _, item := range s.items {
				if strings.HasPrefix(item.Status, StatusInStock) {
					inStockBagURL = fmt.Sprintf("%s/shop/bag", s.Area.ShopOrigin())
					if item.Store.IsAllStores() && item.Status != StatusInStock {
						storeName := strings.TrimPrefix(item.Status, StatusInStock+": ")
						inStockMsg = fmt.Sprintf("%s (%s) %s 有货", item.Store.CityStoreName, storeName, item.Product.Title)
					} else {
						inStockMsg = fmt.Sprintf("%s %s 有货", item.Store.CityStoreName, item.Product.Title)
					}
					hasInStock = true
					s.Status.Set(Pause)
					break
				}
			}
			s.mu.Unlock()

			if hasInStock {
				s.openBrowser(inStockBagURL)
				dialog.ShowInformation("匹配成功", inStockMsg, view.Window)
				view.App.SendNotification(&fyne.Notification{
					Title:   "有货提醒",
					Content: inStockMsg,
				})
				go s.AlertMp3()
				barkBody := inStockMsg + "\n⚡️ 点击通知直接进入官网购物车结算！"
				go s.SendPushNotificationByBark("🎉 Apple Store 有货提醒", barkBody, inStockBagURL)
			}

			if result.blocked {
				resetShopSession()
				consecutiveBlocks++
				continuousSuccessCount = 0
				warmupRoundsLeft = warmupProbationRounds

				jitter := time.Duration(rand.Intn(20)) * time.Second
				backoff := baseBlockedBackoff*time.Duration(consecutiveBlocks) + jitter
				if backoff > maxBlockedBackoff {
					backoff = maxBlockedBackoff
				}
				log.Printf("[风控拦截] Apple/CDN 频率限制 (541), 正在进入精准冷却等待 %v (第 %d 次重试)... 真实解禁窗口通常需 180~240 秒\n", backoff, consecutiveBlocks)

				s.mu.Lock()
				statusLabel := fmt.Sprintf("风控冷却(%ds)", int(backoff.Seconds()))
				for key, item := range s.items {
					if result.blockedStores[item.Store.StoreNumber] || item.Status == StatusWait || item.Status == StatusBlocked {
						s.updateStatusLocked(key, statusLabel)
					}
				}
				s.updateLogStrLocked()
				s.mu.Unlock()

				time.Sleep(backoff)
				continue
			}

			// 正常成功：若此前经历过限流，打印解封并进入保护期
			if consecutiveBlocks > 0 {
				log.Printf("[风控解除] Apple/CDN 封禁已恢复，进入解封回温保护期 (前 %d 轮降频防止瞬间反弹)\n", warmupProbationRounds)
				consecutiveBlocks = 0
			}
			continuousSuccessCount++

			// 轮询间隔休眠
			if warmupRoundsLeft > 0 {
				warmupRoundsLeft--
				warmupDur := warmupInterval + time.Duration(rand.Intn(10))*time.Second
				log.Printf("[回温保护] 当前为解封回温期 (剩余 %d 轮), 本轮保护间隔 %v...\n", warmupRoundsLeft, warmupDur)
				time.Sleep(warmupDur)
			} else {
				// 正常轮询：基准间隔附加 ±25% 动态随机扰动，打破固定周期机器指纹
				baseDur := s.IntervalDuration()
				minDur := time.Duration(float64(baseDur) * 0.75)
				jitterRange := int64(float64(baseDur) * 0.50)
				if jitterRange < 1 {
					jitterRange = 1
				}
				sleepDur := minDur + time.Duration(rand.Int63n(jitterRange))
				time.Sleep(sleepDur)
			}
		}
	}()
}

type skuFetch struct {
	storeNumber    string
	skus           map[string]bool
	inStockDetails map[string]string
	blocked        bool
	empty          bool
}

type storeFetchResult struct {
	skus           map[string]bool
	inStockDetails map[string]string
	blockedStores  map[string]bool
	blocked        bool
}

type queryBatchTarget struct {
	key      string
	location string
	storeNum string
	items    []ListenItem
}

func (s *listenService) groupByStore() storeFetchResult {
	result := storeFetchResult{
		skus:           map[string]bool{},
		inStockDetails: map[string]string{},
		blockedStores:  map[string]bool{},
	}

	defer func() {
		if r := recover(); r != nil {
			log.Println(r)
		}
	}()

	s.mu.RLock()
	// 智能合并：同一城市/Location 的多个具体门店或全城虚拟门店，合并为 1 个 location 请求
	targetMap := map[string]*queryBatchTarget{}
	targetOrder := []string{}

	for _, item := range s.items {
		var targetKey string
		if item.Store.Location != "" {
			targetKey = "LOC:" + item.Store.Location
		} else {
			targetKey = "STORE:" + item.Store.StoreNumber
		}

		if tgt, exists := targetMap[targetKey]; exists {
			tgt.items = append(tgt.items, item)
		} else {
			tgt := &queryBatchTarget{
				key:      targetKey,
				location: item.Store.Location,
				storeNum: item.Store.StoreNumber,
				items:    []ListenItem{item},
			}
			targetMap[targetKey] = tgt
			targetOrder = append(targetOrder, targetKey)
		}
	}
	area := s.Area
	s.mu.RUnlock()

	count := len(targetOrder)
	if count < 1 {
		return result
	}

	sort.Strings(targetOrder)

	for idx, targetKey := range targetOrder {
		tgt := targetMap[targetKey]
		if idx > 0 {
			time.Sleep(staggerDelay)
		}

		// 检查监听状态，若暂停则中止
		stats, ok := s.Status.Get()
		if ok != nil || stats != Running {
			break
		}

		referer := fmt.Sprintf("%s/shop/buy-iphone", area.ShopOrigin())
		var part skuFetch

		if tgt.location != "" {
			link := s.locationURL(tgt.location, tgt.items)
			part = s.fetchLocation(tgt.location, link, referer, tgt.items)
			if part.empty && !part.blocked {
				// 若 location 查询未返回门店（如部分境外无 location 接口），自动降级为单店查询
				for _, it := range tgt.items {
					storeLink := s.fulfillmentURL(it.Store.StoreNumber, []ListenItem{it})
					singlePart := s.fetchSku(it.Store.StoreNumber, storeLink, referer)
					if singlePart.blocked {
						part.blocked = true
						break
					}
					for k, v := range singlePart.skus {
						part.skus[k] = v
					}
				}
			}
		} else {
			link := s.fulfillmentURL(tgt.storeNum, tgt.items)
			part = s.fetchSku(tgt.storeNum, link, referer)
		}

		if part.blocked {
			result.blocked = true
			for _, it := range tgt.items {
				result.blockedStores[it.Store.StoreNumber] = true
			}
			break
		}

		// 实时更新当前批次对应的各商品最新状态到界面
		s.mu.Lock()
		for key, item := range s.items {
			uniqKey := item.Store.StoreNumber + "." + item.Product.Code
			if avail, found := part.skus[uniqKey]; found {
				if avail {
					if detail, hasDetail := part.inStockDetails[uniqKey]; hasDetail && detail != "" {
						s.updateStatusLocked(key, StatusInStock+": "+detail)
					} else {
						s.updateStatusLocked(key, StatusInStock)
					}
				} else {
					s.updateStatusLocked(key, StatusOutStock)
				}
			}
		}
		s.updateLogStrLocked()
		s.mu.Unlock()

		for key, v := range part.skus {
			result.skus[key] = v
		}
		for key, v := range part.inStockDetails {
			result.inStockDetails[key] = v
		}

		// 如果有商品有货，立即停止本轮剩余查询
		hasInStockThisBatch := false
		for _, avail := range part.skus {
			if avail {
				hasInStockThisBatch = true
				break
			}
		}
		if hasInStockThisBatch {
			break
		}
	}

	return result
}

func (s *listenService) fulfillmentURL(storeNumber string, items []ListenItem) string {
	var b strings.Builder
	b.WriteString(s.Area.ShopOrigin())
	b.WriteString("/shop/retail/pickup-message?pl=true")

	for index, item := range items {
		b.WriteString("&parts.")
		b.WriteString(strconv.Itoa(index))
		b.WriteByte('=')
		b.WriteString(item.Product.Code)
	}
	b.WriteString("&store=")
	b.WriteString(storeNumber)

	return b.String()
}

func fulfillmentAvailable(availability gjson.Result) bool {
	if availability.Get("pickupDisplay").String() == "available" {
		return true
	}
	if availability.Get("messageTypes.regular.storeSelectionEnabled").Bool() {
		return true
	}
	return availability.Get("messageTypes.compact.storeSelectionEnabled").Bool()
}

func normalizeLocation(loc string) string {
	loc = strings.TrimSpace(loc)
	switch {
	case strings.Contains(loc, "北京"):
		return "100000"
	case strings.Contains(loc, "上海"):
		return "200000"
	case strings.Contains(loc, "广州"):
		return "510000"
	case strings.Contains(loc, "深圳"):
		return "518000"
	case strings.Contains(loc, "成都"):
		return "610051"
	case strings.Contains(loc, "天津"):
		return "300000"
	case strings.Contains(loc, "重庆"):
		return "400000"
	case strings.Contains(loc, "南京"):
		return "210000"
	case strings.Contains(loc, "杭州"):
		return "310000"
	case strings.Contains(loc, "沈阳"):
		return "110011"
	case strings.Contains(loc, "大连"):
		return "116001"
	case strings.Contains(loc, "昆明"):
		return "650031"
	case strings.Contains(loc, "合肥"):
		return "230031"
	case strings.Contains(loc, "济南"):
		return "250011"
	case strings.Contains(loc, "青岛"):
		return "266000"
	case strings.Contains(loc, "武汉"):
		return "430000"
	case strings.Contains(loc, "长沙"):
		return "410000"
	case strings.Contains(loc, "厦门"):
		return "361000"
	case strings.Contains(loc, "福州"):
		return "350000"
	case strings.Contains(loc, "郑州"):
		return "450000"
	case strings.Contains(loc, "南宁"):
		return "530022"
	case strings.Contains(loc, "无锡"):
		return "214000"
	case strings.Contains(loc, "苏州"):
		return "215000"
	case strings.Contains(loc, "宁波"):
		return "315000"
	case strings.Contains(loc, "温州"):
		return "325000"
	default:
		return loc
	}
}

func (s *listenService) locationURL(location string, items []ListenItem) string {
	var b strings.Builder
	b.WriteString(s.Area.ShopOrigin())
	b.WriteString("/shop/retail/pickup-message?pl=true")

	// 收集去重后的 product codes 并排序保证 URL 稳定性
	codeSet := map[string]bool{}
	for _, item := range items {
		codeSet[item.Product.Code] = true
	}
	codes := make([]string, 0, len(codeSet))
	for code := range codeSet {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for idx, code := range codes {
		b.WriteString("&parts.")
		b.WriteString(strconv.Itoa(idx))
		b.WriteByte('=')
		b.WriteString(code)
	}
	b.WriteString("&location=")
	b.WriteString(url.QueryEscape(normalizeLocation(location)))

	return b.String()
}

func (s *listenService) fetchLocation(location string, skUrl string, referer string, items []ListenItem) skuFetch {
	result := skuFetch{
		skus:           map[string]bool{},
		inStockDetails: map[string]string{},
	}

	warmupShopPage(referer, s.Area.AcceptLanguage())

	resp, err := shopHTTP().R().
		SetHeader("accept", "*/*").
		SetHeader("accept-language", s.Area.AcceptLanguage()).
		SetHeader("referer", referer).
		SetHeader("sec-fetch-dest", "empty").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		Get(skUrl)
	if err != nil {
		log.Println(err)
		return result
	}

	body := resp.String()
	log.Println(resp.GetStatusCode(), skUrl)

	if resp.GetStatusCode() == 541 || resp.GetStatusCode() >= 400 {
		result.blocked = true
		resetShopSession()
		preview := body
		if len(preview) > 160 {
			preview = preview[:160]
		}
		log.Println("fulfillment location blocked", preview)
		return result
	}

	stores := gjson.Get(body, "body.stores")
	if !stores.Exists() || stores.Get("#").Int() == 0 {
		stores = gjson.Get(body, "body.content.pickupMessage.stores")
	}
	if !stores.Exists() || stores.Get("#").Int() == 0 {
		result.empty = true
		log.Println("fulfillment location no stores found for", location)
		return result
	}

	inStockStores := map[string][]string{}

	for _, store := range stores.Array() {
		sn := store.Get("storeNumber").String()
		sname := store.Get("storeName").String()
		for productCode, availability := range store.Get("partsAvailability").Map() {
			uniqKey := fmt.Sprintf("%s.%s", sn, productCode)
			avail := fulfillmentAvailable(availability)
			result.skus[uniqKey] = avail
			if avail {
				inStockStores[productCode] = append(inStockStores[productCode], sname)
			}
		}
	}

	// 针对虚拟“全部/任意门店”任务项
	for _, it := range items {
		if it.Store.IsAllStores() {
			allKey := it.Store.StoreNumber + "." + it.Product.Code
			if names, ok := inStockStores[it.Product.Code]; ok && len(names) > 0 {
				result.skus[allKey] = true
				result.inStockDetails[allKey] = strings.Join(names, "、")
			} else {
				result.skus[allKey] = false
			}
		}
	}

	return result
}

func (s *listenService) fetchSku(storeNumber string, skUrl string, referer string) skuFetch {
	result := skuFetch{
		storeNumber:    storeNumber,
		skus:           map[string]bool{},
		inStockDetails: map[string]string{},
	}

	warmupShopPage(referer, s.Area.AcceptLanguage())

	resp, err := shopHTTP().R().
		SetHeader("accept", "*/*").
		SetHeader("accept-language", s.Area.AcceptLanguage()).
		SetHeader("referer", referer).
		SetHeader("sec-fetch-dest", "empty").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		Get(skUrl)
	if err != nil {
		log.Println(err)
		return result
	}

	body := resp.String()
	log.Println(resp.GetStatusCode(), skUrl)

	if resp.GetStatusCode() == 541 || resp.GetStatusCode() >= 400 {
		result.blocked = true
		preview := body
		if len(preview) > 160 {
			preview = preview[:160]
		}
		log.Println("fulfillment blocked", preview)
		return result
	}

	stores := gjson.Get(body, "body.stores")
	if !stores.Exists() || stores.Get("#").Int() == 0 {
		stores = gjson.Get(body, "body.content.pickupMessage.stores")
	}
	if !stores.Exists() || stores.Get("#").Int() == 0 {
		preview := body
		if len(preview) > 200 {
			preview = preview[:200]
		}
		log.Println("fulfillment unexpected body", preview)
		return result
	}

	for _, store := range stores.Array() {
		for productCode, availability := range store.Get("partsAvailability").Map() {
			uniqKey := fmt.Sprintf("%s.%s", store.Get("storeNumber").String(), productCode)
			result.skus[uniqKey] = fulfillmentAvailable(availability)
		}
	}

	return result
}

// 型号对应预约地址
//func (s *listenService) model2Url(productType string) string {
//	// https://www.apple.com.cn/shop/buy-iphone/iphone-16
//	// https://www.apple.com.cn/shop/buy-iphone/iphone-16-pro
//
//	var t string
//	switch productType {
//	case "iphone16promax", "iphone16pro":
//		t = "iphone-16-pro"
//	case "iphone16":
//		t = "iphone-16"
//	}
//
//	return fmt.Sprintf(
//		"https://www.apple.com/%s/shop/buy-iphone/%s",
//		s.Area.ShortCode,
//		t,
//	)
//}

func (s *listenService) openBrowser(link string) {
	parse, err := url.Parse(link)
	if err != nil {
		dialog.ShowError(err, view.Window)
		return
	}

	err = view.App.OpenURL(parse)
	if err != nil {
		dialog.ShowError(err, view.Window)
		return
	}
}

func (s *listenService) AlertMp3() {
	reader := bytes.NewReader(theme.Mp3().Content())
	streamer, _, err := mp3.Decode(ioutil.NopCloser(reader))
	if err != nil {
		panic(err)
	}
	defer streamer.Close()

	done := make(chan bool)
	speaker.Play(beep.Seq(streamer, beep.Callback(func() {
		done <- true
	})))
	<-done
}

// formatBarkEndpoint 格式化用户填写的 Bark 地址或设备 Key 为规范的推送 API 地址。
func formatBarkEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// 用户只填写了 key
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "https://api.day.app/" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	// 去掉多余路径（例如用户直接复制了带测试文案的地址如 api.day.app/key/内容）
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 0 && parts[0] != "" {
		if strings.Contains(u.Host, "day.app") {
			u.Path = "/" + parts[0]
			u.RawQuery = ""
			return u.String()
		}
	}
	return strings.TrimRight(raw, "/")
}

// BarkPayload Bark 推送数据结构
type BarkPayload struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	URL      string `json:"url"`
	Group    string `json:"group,omitempty"`
	Sound    string `json:"sound,omitempty"`
	Level    string `json:"level,omitempty"`
	Call     int    `json:"call,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Badge    int    `json:"badge,omitempty"`
	AutoCopy int    `json:"autoCopy,omitempty"`
	Copy     string `json:"copy,omitempty"`
}

func (s *listenService) SendPushNotificationByBark(title string, content string, bagUrl string) {
	endpoint := formatBarkEndpoint(s.BarkNotifyUrl)
	if endpoint == "" {
		return
	}

	opt := GetBarkLevelOption(s.BarkLevel)

	payload := BarkPayload{
		Title:    title,
		Body:     content,
		URL:      bagUrl,
		Group:    "Apple Store 预约",
		Icon:     "https://www.apple.com.cn/favicon.ico",
		Sound:    opt.Sound,
		Level:    opt.Level,
		Call:     opt.Call,
		Badge:    1,
		AutoCopy: 1,      // 收到推送时自动复制链接
		Copy:     bagUrl, // 备用：剪贴板直接带有官网购物车直达链接
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[Bark 推送失败] 序列化失败: %v\n", err)
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(data))
	if err != nil {
		log.Printf("[Bark 推送失败] 创建请求失败: %v\n", err)
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Bark 推送失败] 发送失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[Bark 推送失败] HTTP 状态码异常: %d\n", resp.StatusCode)
		return
	}
	log.Printf("[Bark 推送成功] 目标: %s, 跳转链接: %s\n", endpoint, bagUrl)
}
