package hostres

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type Snapshot struct {
	TotalBytes     int64 `json:"total_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
	CPUs           int   `json:"cpus"`
	OK             bool  `json:"ok"`
}

var (
	pageSize  = regexp.MustCompile(`page size of (\d+) bytes`)
	freePages = regexp.MustCompile(`Pages (free|inactive|speculative|purgeable):\s+(\d+)`)
)

func Probe() Snapshot {
	s := Snapshot{
		TotalBytes: sysctlInt("hw.memsize"),
		CPUs:       int(sysctlInt("hw.ncpu")),
	}
	s.AvailableBytes = availableBytes()
	s.OK = s.TotalBytes > 0 && s.AvailableBytes > 0 && s.CPUs > 0
	return s
}

func sysctlInt(key string) int64 {
	out, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func availableBytes() int64 {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0
	}
	text := string(out)
	size := int64(4096)
	if m := pageSize.FindStringSubmatch(text); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			size = n
		}
	}
	var pages int64
	for _, m := range freePages.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseInt(m[2], 10, 64); err == nil {
			pages += n
		}
	}
	return pages * size
}

func (s Snapshot) Fits(need, headroom int64) bool {
	if need <= 0 {
		return true
	}
	if !s.OK {
		return false
	}
	return s.AvailableBytes-need >= headroom
}

func (s Snapshot) FitsCPU(need, inUse int) bool {
	if need <= 0 {
		return true
	}
	if !s.OK {
		return false
	}
	return inUse+need <= s.CPUs
}
