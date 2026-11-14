package data

import (
	"os"
	"path/filepath"

	"factory/go/internal/factorydb"
)

// ResolveDBPath is web/dbpath.py resolve. It is factorydb.ResolvePath (one rule for Go and Python), so the web UI and
// the CLI cannot disagree about which file is the factory database.
func ResolveDBPath(home string) string { return factorydb.ResolvePath(home) }

// FactoryHome is $FACTORY_HOME, else ~/factory (the only inputs that pick the database).
func FactoryHome() string {
	if h := os.Getenv("FACTORY_HOME"); h != "" {
		return h
	}
	uh, err := os.UserHomeDir()
	if err != nil {
		uh = "."
	}
	return filepath.Join(uh, "factory")
}

// DBPath is web/context.py db_path: the database of FactoryHome().
func DBPath() string { return ResolveDBPath(FactoryHome()) }
