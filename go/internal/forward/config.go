package forward

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ConfigEnv overrides the config file path (tests, or a non-default install).
const ConfigEnv = "FORWARD_CONFIG"

// maxConfig bounds the config file read.
const maxConfig = 1 << 20

// Config is ~/.config/hivemill/forward.json. The client (worker host) uses hq_host and remote_receiver; the
// server (HQ host) uses factory_bin and path_map. One file may hold all keys.
type Config struct {
	HQHost         string     `json:"hq_host"`
	RemoteReceiver string     `json:"remote_receiver"`
	PathMap        [][]string `json:"path_map"`
	FactoryBin     string     `json:"factory_bin"`
}

var (
	// hostRe: an ssh destination ([user@]host or an ssh_config alias); never starts with '-' (no option injection).
	hostRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(@[A-Za-z0-9_][A-Za-z0-9._-]*)?$`)
	// receiverRe: ssh hands the remote command to the remote shell, so only plain path characters pass.
	receiverRe = regexp.MustCompile(`^[A-Za-z0-9_/~.][A-Za-z0-9_/~.-]*$`)
)

// ConfigPath is $FORWARD_CONFIG, else $HOME/.config/hivemill/forward.json.
func ConfigPath(environ []string) (string, error) {
	if p := lookup(environ, ConfigEnv); p != "" {
		return p, nil
	}
	home := lookup(environ, "HOME")
	if home == "" {
		return "", refuse("no HOME and no %s: cannot find forward.json", ConfigEnv)
	}
	return filepath.Join(home, ".config", "hivemill", "forward.json"), nil
}

// LoadConfig reads and strictly parses the config (unknown keys refused) and checks path_map.
func LoadConfig(path string) (Config, error) {
	var c Config
	f, err := os.Open(path)
	if err != nil {
		return c, refuse("config %s: %v (see docs/multi-machine.md, Install)", path, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxConfig+1))
	if err != nil {
		return c, refuse("config %s: %v", path, err)
	}
	if len(raw) > maxConfig {
		return c, refuse("config %s: larger than %d bytes", path, maxConfig)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, refuse("config %s: %v", path, err)
	}
	for i, m := range c.PathMap {
		if len(m) != 2 {
			return Config{}, refuse("config %s: path_map[%d] must be [from, to]", path, i)
		}
		for _, p := range m {
			if !filepath.IsAbs(p) {
				return Config{}, refuse("config %s: path_map[%d]: %q is not an absolute path", path, i, p)
			}
		}
	}
	return c, nil
}

// ValidateClient checks the keys the client needs. An hq_host containing "factory" is refused (G-057 R2).
func (c Config) ValidateClient() error {
	if c.HQHost == "" || c.RemoteReceiver == "" {
		return refuse("config: hq_host and remote_receiver are required on the client (see docs/multi-machine.md)")
	}
	if !hostRe.MatchString(c.HQHost) {
		return refuse("config: hq_host %q is not a plain ssh destination", c.HQHost)
	}
	if strings.Contains(strings.ToLower(c.HQHost), "factory") {
		return refuse("config: hq_host %q must not contain \"factory\" (G-057 R2: host names)", c.HQHost)
	}
	if !receiverRe.MatchString(c.RemoteReceiver) {
		return refuse("config: remote_receiver %q must be a plain path (letters, digits, / ~ . _ -)", c.RemoteReceiver)
	}
	return nil
}

// ValidateServer checks the keys the server needs.
func (c Config) ValidateServer() error {
	if !filepath.IsAbs(c.FactoryBin) {
		return refuse("config: factory_bin must be the absolute path of bin/factory on the HQ host, got %q", c.FactoryBin)
	}
	return nil
}
