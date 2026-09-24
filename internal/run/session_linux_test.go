//go:build linux

package run

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func supervisorSessionID(pid int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return 0, fmt.Errorf("missing process name in /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 4 {
		return 0, fmt.Errorf("missing session ID in /proc/%d/stat", pid)
	}
	return strconv.Atoi(fields[3])
}
