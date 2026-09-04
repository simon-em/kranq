package daemon

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func EnvPath(home string) string { return filepath.Join(home, "env") }

func LoadEnv(home string) (map[string]string, error) {
	f, err := os.Open(EnvPath(home))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || name == "" {
			continue
		}
		out[strings.TrimSpace(name)] = value
	}
	return out, scan.Err()
}

func SetEnv(home, name, value string) error {
	current, err := LoadEnv(home)
	if err != nil {
		return err
	}
	if value == "" {
		delete(current, name)
	} else {
		current[name] = value
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	names := make([]string, 0, len(current))
	for k := range current {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		fmt.Fprintf(&b, "%s=%s\n", k, current[k])
	}
	path := EnvPath(home)
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
