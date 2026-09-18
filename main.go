package main

import (
	"apple-store-helper/common"
	"apple-store-helper/services"
	"apple-store-helper/theme"
	"apple-store-helper/view"
	"errors"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"github.com/faiface/beep"
	"github.com/faiface/beep/speaker"
)

// main 主函数 (Main function)
func main() {
	initMP3Player()
	initFyneApp()

	// 默认地区 (Default Area)
	defaultArea := services.Listen.Area.Title

	// 门店选择器 (Store Selector)
	storeWidget := widget.NewSelect(services.Store.ByAreaTitleForOptions(defaultArea), nil)
	storeWidget.PlaceHolder = "请选择自提门店"

	// 型号选择器 (Product Selector)
	productWidget := widget.NewSelect(services.Product.ByAreaTitleForOptions(defaultArea), nil)
	productWidget.PlaceHolder = "请选择 iPhone 型号"

	// Bark 通知输入框
	barkWidget := widget.NewEntry()
	barkWidget.SetPlaceHolder("https://api.day.app/你的BarkKey 或 直接输入Key")
	barkWidget.OnChanged = func(val string) {
		services.Listen.BarkNotifyUrl = val
	}

	// Bark 提醒级别选择器 (Bark Level Selector)
	barkLevelWidget := widget.NewSelect(services.BarkLevelLabels(), func(val string) {
		services.Listen.BarkLevel = val
	})
	barkLevelWidget.SetSelected(services.BarkLevelOptions[0].Label)

	// 网络代理设置输入框 (Proxy Setting Entry)
	proxyWidget := widget.NewEntry()
	proxyWidget.SetPlaceHolder("可选: http://127.0.0.1:7897 或 socks5://127.0.0.1:7897 (留空直连)")
	proxyWidget.OnChanged = func(val string) {
		services.SetCustomProxy(val)
	}

	// 查询频率选择器 (Polling Interval Selector)
	intervalWidget := widget.NewSelect(services.IntervalLabels(), func(val string) {
		services.Listen.IntervalSeconds.Set(services.ParseIntervalLabel(val))
	})
	intervalWidget.SetSelected(services.IntervalLabel(services.DefaultIntervalSeconds))

	// 地区选择器 (Area Selector)
	areaWidget := widget.NewRadioGroup(services.Area.ForOptions(), func(value string) {
		// 防止空值或无效值导致崩溃
		if value == "" {
			return
		}

		storeWidget.Options = services.Store.ByAreaTitleForOptions(value)
		storeWidget.ClearSelected()

		productWidget.Options = services.Product.ByAreaTitleForOptions(value)
		productWidget.ClearSelected()

		services.Listen.Area = services.Area.GetArea(value)
		services.Listen.Clean()
	})

	areaWidget.Horizontal = true

	help := `1. 在 Apple 官网将需要购买的型号加入购物车
2. 选择地区、门店和型号，点击“添加”按钮，将需要监听的型号添加到监听列表
3. 如遇到 541 频率拦截，可填入本地代理端口 (如 http://127.0.0.1:7897) 瞬间换 IP 畅快监听
4. 点击“开始”按钮开始监听，检测到有货时会自动打开购物车页面
`

	loadUserSettingsCache(areaWidget, storeWidget, productWidget, barkWidget, barkLevelWidget, proxyWidget, intervalWidget)

	// 初始化 GUI 窗口内容 (Initialize GUI)
	view.Window.SetContent(container.NewVBox(
		widget.NewLabel(help),
		container.New(layout.NewFormLayout(), widget.NewLabel("选择地区:"), areaWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("选择门店:"), storeWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("选择型号:"), productWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("查询频率:"), intervalWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("网络代理:"), proxyWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("Bark 通知地址:"), barkWidget),
		container.New(layout.NewFormLayout(), widget.NewLabel("Bark 提醒级别:"), barkLevelWidget),

		container.NewBorder(nil, nil,
			createActionButtons(areaWidget, storeWidget, productWidget, barkWidget, barkLevelWidget, proxyWidget, intervalWidget),
			createControlButtons(),
		),

		services.Listen.Logs,
		layout.NewSpacer(),
		createVersionLabel(),
	))

	view.Window.Resize(fyne.NewSize(1000, 800))
	view.Window.CenterOnScreen()
	services.Listen.Run()
	view.Window.ShowAndRun()
}

// initMP3Player 初始化 MP3 播放器 (Initialize MP3 player)
func initMP3Player() {
	SampleRate := beep.SampleRate(44100)
	speaker.Init(SampleRate, SampleRate.N(time.Second/10))
}

// initFyneApp 初始化 Fyne 应用 (Initialize Fyne App)
func initFyneApp() {
	view.App = app.NewWithID("apple-store-helper")
	view.App.Settings().SetTheme(&theme.MyTheme{})
	view.Window = view.App.NewWindow("Apple Store Helper")
}

// 加载用户设置缓存 (Load user settings cache)
func loadUserSettingsCache(areaWidget *widget.RadioGroup, storeWidget *widget.Select, productWidget *widget.Select, barkNotifyWidget *widget.Entry, barkLevelWidget *widget.Select, proxyWidget *widget.Entry, intervalWidget *widget.Select) {
	settings, err := services.LoadSettings()
	interval := services.DefaultIntervalSeconds
	if err == nil {
		areaWidget.SetSelected(settings.SelectedArea)
		storeWidget.SetSelected(settings.SelectedStore)
		productWidget.SetSelected(settings.SelectedProduct)
		services.Listen.SetListenItems(settings.ListenItems)
		barkNotifyWidget.SetText(settings.BarkNotifyUrl)
		services.Listen.BarkNotifyUrl = settings.BarkNotifyUrl
		if settings.BarkLevel != "" {
			barkLevelWidget.SetSelected(settings.BarkLevel)
			services.Listen.BarkLevel = settings.BarkLevel
		} else {
			barkLevelWidget.SetSelected(services.BarkLevelOptions[0].Label)
			services.Listen.BarkLevel = services.BarkLevelOptions[0].Label
		}
		if settings.ProxyURL != "" {
			proxyWidget.SetText(settings.ProxyURL)
			services.SetCustomProxy(settings.ProxyURL)
		}
		if settings.ListenInterval >= 5 {
			interval = settings.ListenInterval
		} else {
			interval = services.DefaultIntervalSeconds
		}
	} else {
		areaWidget.SetSelected(services.Listen.Area.Title)
		barkLevelWidget.SetSelected(services.BarkLevelOptions[0].Label)
		services.Listen.BarkLevel = services.BarkLevelOptions[0].Label
	}

	services.Listen.IntervalSeconds.Set(interval)
	intervalWidget.SetSelected(services.IntervalLabel(interval))
}

// 创建动作按钮 (Create action buttons)
func createActionButtons(areaWidget *widget.RadioGroup, storeWidget *widget.Select, productWidget *widget.Select, barkNotifyWidget *widget.Entry, barkLevelWidget *widget.Select, proxyWidget *widget.Entry, intervalWidget *widget.Select) *fyne.Container {
	return container.NewHBox(
		widget.NewButton("添加", func() {
			if storeWidget.Selected == "" || productWidget.Selected == "" {
				dialog.ShowError(errors.New("请选择门店和型号"), view.Window)
			} else {
				services.Listen.Add(areaWidget.Selected, storeWidget.Selected, productWidget.Selected, barkNotifyWidget.Text, barkLevelWidget.Selected)
				services.SetCustomProxy(proxyWidget.Text)
				services.SaveSettings(services.UserSettings{
					SelectedArea:    areaWidget.Selected,
					SelectedStore:   storeWidget.Selected,
					SelectedProduct: productWidget.Selected,
					BarkNotifyUrl:   barkNotifyWidget.Text,
					BarkLevel:       barkLevelWidget.Selected,
					ProxyURL:        proxyWidget.Text,
					ListenInterval:  services.ParseIntervalLabel(intervalWidget.Selected),
					ListenItems:     services.Listen.GetListenItems(),
				})
			}
		}),
		widget.NewButton("清空", func() {
			services.Listen.Clean()
			services.ClearSettings()
		}),
		widget.NewButton("测试代理", func() {
			proxyText := strings.TrimSpace(proxyWidget.Text)
			if proxyText == "" {
				dialog.ShowInformation("提示", "当前未配置代理 (将使用系统/直连网络)", view.Window)
				return
			}
			go func() {
				err := services.TestProxyConnection(proxyText)
				if err != nil {
					dialog.ShowError(err, view.Window)
				} else {
					dialog.ShowInformation("代理可用", fmt.Sprintf("代理 %s 连接 Apple Store 成功 (HTTP 200)！", proxyText), view.Window)
				}
			}()
		}),
		widget.NewButton("试听(有货提示音)", func() {
			go services.Listen.AlertMp3()
		}),
		widget.NewButton("测试 Bark 通知", func() {
			services.Listen.BarkNotifyUrl = barkNotifyWidget.Text
			services.Listen.BarkLevel = barkLevelWidget.Selected
			if strings.TrimSpace(barkNotifyWidget.Text) == "" {
				dialog.ShowInformation("提示", "请先输入 Bark 通知地址或 Key", view.Window)
				return
			}
			bagURL := fmt.Sprintf("%s/shop/bag", services.Listen.Area.ShopOrigin())
			go services.Listen.SendPushNotificationByBark(
				"🎉【测试】Apple Store 预约助手",
				fmt.Sprintf("收到此通知说明 Bark 配置成功！\n当前提醒级别: %s\n⚡️ 点击本条通知将直接跳转到 Apple 官网购物车购买。", barkLevelWidget.Selected),
				bagURL,
			)
			dialog.ShowInformation("Bark 推送已发送", "测试通知已向手机发送，请在 iPhone 上查收并点击通知测试是否能打开官网购物车。", view.Window)
		}),
	)
}

// 创建控制按钮 (Create control buttons)
func createControlButtons() *fyne.Container {
	return container.NewHBox(
		widget.NewButton("开始", func() {
			services.Listen.Status.Set(services.Running)
		}),
		widget.NewButton("暂停", func() {
			services.Listen.Status.Set(services.Pause)
		}),
		container.NewCenter(widget.NewLabel("状态:")),
		container.NewCenter(widget.NewLabelWithData(services.Listen.Status)),
	)
}

// createVersionLabel 创建版本标签 (Create version label)
func createVersionLabel() *fyne.Container {
	return container.NewHBox(
		layout.NewSpacer(),
		widget.NewLabel("version: "+common.VERSION),
	)
}
