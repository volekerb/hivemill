package cli

import "factory/go/internal/store"

// ConfigDefault is one config key with its default (the generated table lives in internal/store).
type ConfigDefault = store.ConfigDefault

// DefaultConfig lists every config key and its default, in DEFAULT_CONFIG order.
var DefaultConfig = store.DefaultConfig

func defaultMap() map[string]string { return store.DefaultMap() }
