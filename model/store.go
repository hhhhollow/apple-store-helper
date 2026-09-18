package model

import "strings"

const AllStoresPrefix = "ALL_"

type Store struct {
	StoreNumber   string
	CityStoreName string
	Location      string
}

func (s Store) IsAllStores() bool {
	return strings.HasPrefix(s.StoreNumber, AllStoresPrefix) || s.StoreNumber == "ALL"
}