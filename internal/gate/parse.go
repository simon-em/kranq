package gate

import (
	"regexp"
	"strconv"
	"time"
)

var marker = regexp.MustCompile(`KRANQ-GATE exhausted resets_at=(\d+) window=(\S+)`)

func ParseExhaustion(log string) (resetsAt time.Time, window string, found bool) {
	m := marker.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return time.Time{}, "", false
	}
	last := m[len(m)-1]
	seconds, err := strconv.ParseInt(last[1], 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, last[2], true
	}
	return time.Unix(seconds, 0), last[2], true
}
