// Package kit holds the code shared by the Hivemill Kit binaries (ADR 0007): the strict config loader, the fixed
// exit codes and the --version helper. It imports nothing outside the standard library, so a kit binary never
// pulls in the factory DB.
package kit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Exit codes of every kit binary (ADR 0007 section F).
const (
	OK      = 0 // success
	Err     = 1 // error
	Usage   = 2 // usage error, or a hook blocking
	Refused = 3 // refused by a gate
)

// Version is set at build time with -ldflags "-X factory/go/internal/kit.Version=vX.Y.Z".
var Version = "dev"

// PrintVersion writes "<binary> <version>".
func PrintVersion(w io.Writer, binary string) {
	fmt.Fprintf(w, "%s %s\n", binary, Version)
}

// HandleVersion prints the version line and returns true when args is exactly "--version".
func HandleVersion(w io.Writer, binary string, args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		PrintVersion(w, binary)
		return true
	}
	return false
}

// LoadConfig reads the JSON file at path into v. It requires an integer "schemaVersion" >= 1, refuses a value above
// maxSchema and rejects unknown fields (v should declare a SchemaVersion field tagged "schemaVersion").
func LoadConfig(path string, maxSchema int, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config %s: %w", path, err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("config %s: not a JSON object: %w", path, err)
	}
	raw, ok := probe["schemaVersion"]
	if !ok {
		return fmt.Errorf("config %s: schemaVersion is required (integer >= 1)", path)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil || n < 1 {
		return fmt.Errorf("config %s: schemaVersion must be an integer >= 1", path)
	}
	if n > maxSchema {
		return fmt.Errorf("config %s: schemaVersion %d is newer than this binary supports (max %d); upgrade hivemill", path, n, maxSchema)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("config %s: unexpected data after the JSON object", path)
	}
	return nil
}
