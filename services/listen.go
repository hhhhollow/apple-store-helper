package services

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
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

	DefaultIntervalSeconds = 3
	staggerDelay           = 150 * time.Millisecond
	blockedBackoff         = 5 * time.Second
)

// IntervalOptions 用户可选的轮询间隔档位(秒)。
// 1s 偏激进,容易触发 Apple 限流;>=3s 是常见安全档位;30/60s 适合长期挂机。
var IntervalOptions = []int{1, 3, 5, 10, 30, 60}

// IntervalLabel 把秒数转成下拉框显示文案。
func IntervalLabel(seconds int) string {
	if seconds == DefaultIntervalSeconds {
		return fmt.Sprintf("%d 秒 (默认)", seconds)
	}
	return fmt.Sprintf("%d 秒", seconds)
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

var Listen = listenService{
	items:           map[string]ListenItem{},
	Status:          binding.NewString(),
	Area:            model.Areas[0],
	Logs:            widget.NewLabel(""),
	IntervalSeconds: binding.NewInt(),
}

type listenService struct {
	mu              sync.RWMutex
	items           map[string]ListenItem
	Status          binding.String
	Area            model.Area
	Logs            *widget.Label
	BarkNotifyUrl   string
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
	if err != nil || seconds <= 0 {
		seconds = DefaultIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (s *listenService) Add(areaTitle string, storeTitle string, productTitle string, barkNotifyUrl string) {
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

		for {
			stats, ok := s.Status.Get()
			running := ok == nil && stats == Running

			s.mu.RLock()
			itemCount := len(s.items)
			s.mu.RUnlock()

			if !running || itemCount == 0 {
				time.Sleep(idleSleep)
				continue
			}

			result := s.groupByStore()

			s.mu.Lock()
			hasInStock := false
			var inStockBagURL string
			var inStockMsg string

			for key, item := range s.items {
				status, ok := result.skus[item.Store.StoreNumber+"."+item.Product.Code]
				if !ok {
					if result.blockedStores[item.Store.StoreNumber] {
						s.updateStatusLocked(key, StatusBlocked)
					} else if item.Status == "" {
						s.updateStatusLocked(key, StatusWait)
					}
					continue
				}

				if status {
					s.updateStatusLocked(key, StatusInStock)
					s.Status.Set(Pause)

					inStockBagURL = fmt.Sprintf("%s/shop/bag", s.Area.ShopOrigin())
					inStockMsg = fmt.Sprintf("%s %s 有货", item.Store.CityStoreName, item.Product.Title)
					hasInStock = true
					break
				}
				s.updateStatusLocked(key, StatusOutStock)
			}

			s.updateLogStrLocked()
			s.mu.Unlock()

			if hasInStock {
				s.openBrowser(inStockBagURL)
				dialog.ShowInformation("匹配成功", inStockMsg, view.Window)
				view.App.SendNotification(&fyne.Notification{
					Title:   "有货提醒",
					Content: inStockMsg,
				})
				go s.AlertMp3()
				go s.SendPushNotificationByBark("有货提醒", inStockMsg, inStockBagURL)
			}

			if result.blocked {
				resetShopSession()
				time.Sleep(blockedBackoff)
				continue
			}

			time.Sleep(s.IntervalDuration())
		}
	}()
}

type skuFetch struct {
	storeNumber string
	skus        map[string]bool
	blocked     bool
}

type storeFetchResult struct {
	skus          map[string]bool
	blockedStores map[string]bool
	blocked       bool
}

func (s *listenService) groupByStore() storeFetchResult {
	result := storeFetchResult{
		skus:          map[string]bool{},
		blockedStores: map[string]bool{},
	}

	defer func() {
		if r := recover(); r != nil {
			log.Println(r)
		}
	}()

	s.mu.RLock()
	group := map[string][]ListenItem{}
	for _, item := range s.items {
		group[item.Store.StoreNumber] = append(group[item.Store.StoreNumber], item)
	}
	area := s.Area
	s.mu.RUnlock()

	count := len(group)
	if count < 1 {
		return result
	}

	idx := 0
	for storeNumber, items := range group {
		if idx > 0 {
			time.Sleep(staggerDelay)
		}
		idx++
		link := s.fulfillmentURL(storeNumber, items)
		referer := productBuyURL(area, items[0].Product)
		part := s.fetchSku(storeNumber, link, referer)
		if part.blocked {
			result.blocked = true
			if part.storeNumber != "" {
				result.blockedStores[part.storeNumber] = true
			}
		}
		for key, v := range part.skus {
			result.skus[key] = v
		}
	}

	return result
}

func (s *listenService) fulfillmentURL(storeNumber string, items []ListenItem) string {
	var b strings.Builder
	b.WriteString(s.Area.ShopOrigin())
	b.WriteString("/shop/retail/pickup-message?little=true&store=")
	b.WriteString(storeNumber)

	for index, item := range items {
		b.WriteString("&parts.")
		b.WriteString(strconv.Itoa(index))
		b.WriteByte('=')
		b.WriteString(item.Product.Code)
	}

	return b.String()
}

func familyPath(familyType string) string {
	switch familyType {
	case "iphone18pro", "iphone18promax":
		return "iphone-18-pro"
	case "iphoneduo":
		return "iphone-duo"
	case "iphone17pro", "iphone17promax":
		return "iphone-17-pro"
	case "iphoneair":
		return "iphone-air"
	case "iphone17":
		return "iphone-17"
	default:
		if strings.HasPrefix(familyType, "iphone") {
			return "iphone-" + strings.TrimPrefix(familyType, "iphone")
		}
		return familyType
	}
}

func productBuyURL(area model.Area, product model.Product) string {
	return fmt.Sprintf("%s/shop/buy-iphone/%s/%s", area.ShopOrigin(), familyPath(product.Type), strings.ToLower(product.Code))
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

func (s *listenService) fetchSku(storeNumber string, skUrl string, referer string) skuFetch {
	result := skuFetch{
		storeNumber: storeNumber,
		skus:        map[string]bool{},
	}

	warmupShopPage(referer, s.Area.AcceptLanguage())

	resp, err := shopHTTP().R().
		SetHeader("accept", "*/*").
		SetHeader("accept-language", s.Area.AcceptLanguage()).
		SetHeader("cache-control", "no-cache").
		SetHeader("pragma", "no-cache").
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

func (s *listenService) SendPushNotificationByBark(title string, content string, bagUrl string) {

	if len(s.BarkNotifyUrl) <= 0 {
		return
	}

	apiUrl := fmt.Sprintf("%s/%s/%s?url=%s", strings.TrimRight(s.BarkNotifyUrl, "/"), title, content, bagUrl)

	response, err := http.Get(apiUrl)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
}
