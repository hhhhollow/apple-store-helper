package services

import (
	"sync"
	"testing"
	"time"

	"apple-store-helper/model"
)

func TestStaggeredMultiStore(t *testing.T) {
	Listen.Clean()
	Listen.items = map[string]ListenItem{
		"R320.MJY94CH/A": {Store: model.Store{StoreNumber: "R320", CityStoreName: "北京-三里屯"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R388.MJY94CH/A": {Store: model.Store{StoreNumber: "R388", CityStoreName: "北京-西单大悦城"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R448.MJY94CH/A": {Store: model.Store{StoreNumber: "R448", CityStoreName: "北京-王府井"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R479.MJY94CH/A": {Store: model.Store{StoreNumber: "R479", CityStoreName: "北京-华贸购物中心"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
		"R645.MJY94CH/A": {Store: model.Store{StoreNumber: "R645", CityStoreName: "北京-朝阳大悦城"}, Product: model.Product{Type: "iphone18promax", Code: "MJY94CH/A"}},
	}
	Listen.Area = model.Areas[0] // 中国大陆

	for round := 0; round < 3; round++ {
		start := time.Now()
		res := Listen.groupByStore()
		dur := time.Since(start)

		if res.blocked {
			t.Fatalf("Round %d: unexpectedly blocked by Apple", round)
		}
		if len(res.skus) != 5 {
			t.Fatalf("Round %d: expected 5 SKUs, got %d", round, len(res.skus))
		}
		for store, isBlocked := range res.blockedStores {
			if isBlocked {
				t.Fatalf("Round %d: store %s was marked blocked", round, store)
			}
		}
		t.Logf("Round %d PASS: got 5/5 stores in %v", round, dur)
		time.Sleep(1 * time.Second)
	}
}

func TestThreadSafety(t *testing.T) {
	Listen.Clean()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			Listen.UpdateStatus("R320.MJY94CH/A", StatusOutStock)
			_ = Listen.GetListenItems()
			Listen.UpdateLogStr()
		}(i)
	}

	wg.Wait()
}
