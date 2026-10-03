package tools

import (
	"fmt"
	"os"
	"time"
)

// readImageFile polls for the exported PNG (export is synchronous but the file
// write may lag), reads it, and removes it.
func readImageFile(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			data, err := os.ReadFile(path)
			if err == nil {
				_ = os.Remove(path)
				return data, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("screenshot export did not produce %s", path)
}
