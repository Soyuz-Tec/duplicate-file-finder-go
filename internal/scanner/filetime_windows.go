//go:build windows

package scanner

import (
	"syscall"
	"time"
	"unsafe"
)

const windowsToUnixEpochTicks = 116444736000000000

func bestEffortCreatedAt(path string, fallback time.Time) time.Time {
	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fallback
	}

	var data syscall.Win32FileAttributeData
	err = syscall.GetFileAttributesEx(ptr, 0, (*byte)(unsafe.Pointer(&data)))
	if err != nil {
		return fallback
	}

	ticks := int64(uint64(data.CreationTime.HighDateTime)<<32 | uint64(data.CreationTime.LowDateTime))
	if ticks <= windowsToUnixEpochTicks {
		return fallback
	}
	return time.Unix(0, (ticks-windowsToUnixEpochTicks)*100)
}
