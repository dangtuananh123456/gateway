//go:build windows

package monitor

import (
	"syscall"
	"time"
)

func processCPUTime() (time.Duration, error) {
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, err
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return 0, err
	}
	return filetimeToDuration(kernel) + filetimeToDuration(user), nil
}

func filetimeToDuration(ft syscall.Filetime) time.Duration {
	return time.Duration((int64(ft.HighDateTime)<<32 + int64(ft.LowDateTime)) * 100)
}
