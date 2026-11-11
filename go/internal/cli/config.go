package cli

// ConfigDefault is one config key with its default.
type ConfigDefault struct{ Key, Value string }

func defaultMap() map[string]string {
	m := make(map[string]string, len(DefaultConfig))
	for _, d := range DefaultConfig {
		m[d.Key] = d.Value
	}
	return m
}
