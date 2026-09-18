package services

import (
	"apple-store-helper/config"
	"apple-store-helper/model"
	"fmt"
	"sort"
	"strings"

	"github.com/thoas/go-funk"
	"github.com/tidwall/gjson"
)

var Store = storeService{
	stores: map[string][]model.Store{},
}

type storeService struct {
	stores map[string][]model.Store
}

func (s *storeService) ByArea(area model.Area) []model.Store {
	stores, err := config.ReadConfigFile("stores.json")
	if err != nil {
		panic(err)
	}

	for _, v := range gjson.ParseBytes(stores).Array() {
		locale := v.Get("locale").String()
		hasStates := v.Get("hasStates").Bool()

		localeStores := []model.Store{}

		type cityGroup struct {
			displayName string
			location    string
			count       int
		}
		groups := map[string]*cityGroup{}

		if hasStates {
			for _, state := range v.Get("state").Array() {
				stateName := state.Get("name").String()
				for _, store := range state.Get("store").Array() {
					stateAddr := store.Get("address.stateName").String()
					if stateAddr == "" {
						stateAddr = stateName
					}
					city := store.Get("address.city").String()
					loc := strings.TrimSpace(stateAddr + " " + city)
					localeStores = append(localeStores, model.Store{
						StoreNumber:   store.Get("id").String(),
						CityStoreName: fmt.Sprintf("%s-%s", stateAddr, store.Get("name").String()),
						Location:      loc,
					})

					displayName := stateAddr
					if stateAddr != city && city != "" {
						displayName = fmt.Sprintf("%s-%s", stateAddr, city)
					}
					if g, ok := groups[loc]; ok {
						g.count++
					} else {
						groups[loc] = &cityGroup{
							displayName: displayName,
							location:    loc,
							count:       1,
						}
					}
				}
			}
		} else {
			for _, store := range v.Get("store").Array() {
				city := store.Get("address.city").String()
				localeStores = append(localeStores, model.Store{
					StoreNumber:   store.Get("id").String(),
					CityStoreName: fmt.Sprintf("%s-%s", city, store.Get("name").String()),
					Location:      city,
				})
				if g, ok := groups[city]; ok {
					g.count++
				} else {
					groups[city] = &cityGroup{
						displayName: city,
						location:    city,
						count:       1,
					}
				}
			}
		}

		// 去重具体门店
		localeStores = funk.UniqBy(localeStores, func(x model.Store) string {
			return x.StoreNumber
		}).([]model.Store)

		// 聚合生成各城市/区域的“全部/任意门店”虚拟 Store
		var allStores []model.Store
		for loc, g := range groups {
			if g.count >= 2 || (locale == "zh_CN" && g.location != "") {
				allStores = append(allStores, model.Store{
					StoreNumber:   model.AllStoresPrefix + g.displayName,
					CityStoreName: fmt.Sprintf("%s - [全部/任意门店]", g.displayName),
					Location:      loc,
				})
			}
		}
		sort.Slice(allStores, func(i, j int) bool {
			return allStores[i].CityStoreName < allStores[j].CityStoreName
		})

		s.stores[locale] = append(allStores, localeStores...)
	}

	return s.stores[area.Locale]
}

func (s *storeService) ByAreaTitleForOptions(areaTitle string) []string {
	area := Area.GetArea(areaTitle)
	allList := s.ByArea(area)

	var virtualOptions []string
	var regularOptions []string

	for _, store := range allList {
		if store.IsAllStores() {
			virtualOptions = append(virtualOptions, store.CityStoreName)
		} else {
			regularOptions = append(regularOptions, store.CityStoreName)
		}
	}

	sort.Strings(virtualOptions)
	sort.Strings(regularOptions)

	return append(virtualOptions, regularOptions...)
}

func (s *storeService) GetStore(areaTitle string, storeTitle string) model.Store {
	code := Area.Title2Code(areaTitle)
	s.ensureLoaded(code)

	found := funk.Find(s.stores[code], func(x model.Store) bool {
		return x.CityStoreName == storeTitle
	})
	if found == nil {
		return model.Store{CityStoreName: storeTitle}
	}
	return found.(model.Store)
}

func (s *storeService) GetByNumber(locale string, storeNumber string) model.Store {
	s.ensureLoaded(locale)
	found := funk.Find(s.stores[locale], func(x model.Store) bool {
		return x.StoreNumber == storeNumber
	})
	if found == nil {
		return model.Store{StoreNumber: storeNumber}
	}
	return found.(model.Store)
}

func (s *storeService) ensureLoaded(locale string) {
	if len(s.stores[locale]) > 0 {
		return
	}
	for _, area := range model.Areas {
		if area.Locale == locale {
			s.ByArea(area)
			return
		}
	}
}
