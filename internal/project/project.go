package project

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/effetmonstre/forge/internal/task"
)

const (
	SetupFile   = "ci/setup.yaml"
	BasekeyFile = "ci/basekey.txt"
)

type Setup struct {
	Memory string `yaml:"memory"`
	CPUs   int    `yaml:"cpus"`
	Disk   string `yaml:"disk"`
	Script string `yaml:"setup"`
}

type Project struct {
	Setup      Setup
	SetupRaw   []byte
	BasekeyRaw []byte
	Basekey    []KeyFile
}

type KeyFile struct {
	Path     string
	Contents []byte
	Missing  bool
}

func Load(checkout string) (Project, error) {
	var p Project
	raw, err := os.ReadFile(filepath.Join(checkout, SetupFile))
	if err != nil {
		if os.IsNotExist(err) {
			return p, fmt.Errorf("%s not found in the checkout", SetupFile)
		}
		return p, err
	}
	p.SetupRaw = raw
	if err := yaml.Unmarshal(raw, &p.Setup); err != nil {
		return p, fmt.Errorf("invalid %s: %w", SetupFile, err)
	}
	if strings.TrimSpace(p.Setup.Script) == "" {
		return p, fmt.Errorf("%s has no setup: block", SetupFile)
	}
	if p.Setup.Memory != "" {
		if _, err := task.ParseMemory(p.Setup.Memory); err != nil {
			return p, fmt.Errorf("invalid %s: %w", SetupFile, err)
		}
	}
	p.BasekeyRaw, p.Basekey, err = loadBasekey(checkout)
	return p, err
}

func loadBasekey(checkout string) ([]byte, []KeyFile, error) {
	raw, err := os.ReadFile(filepath.Join(checkout, BasekeyFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var files []KeyFile
	scan := bufio.NewScanner(bytes.NewReader(raw))
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(checkout, line))
		files = append(files, KeyFile{Path: line, Contents: contents, Missing: err != nil})
	}
	return raw, files, scan.Err()
}

func (s Setup) MemoryGiB() (float64, bool) { return gib(s.Memory) }

func (s Setup) DiskGiB() (float64, bool) { return gib(s.Disk) }

func gib(v string) (float64, bool) {
	if v == "" {
		return 0, false
	}
	n, err := task.ParseMemory(v)
	if err != nil {
		return 0, false
	}
	return float64(n) / (1 << 30), true
}
