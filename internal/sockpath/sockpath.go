package sockpath

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const Max = 100

func For(preferred string) string {
	if len(preferred) <= Max {
		return preferred
	}
	sum := sha256.Sum256([]byte(preferred))
	short := filepath.Join(os.TempDir(), fmt.Sprintf("kranq-%d-%x-%s", os.Getuid(), sum[:4], filepath.Base(preferred)))
	if len(short) <= Max {
		return short
	}
	return fmt.Sprintf("/tmp/kranq-%d-%x.sock", os.Getuid(), sum[:6])
}
