//go:build !windows

package scanner

import "time"

func bestEffortCreatedAt(_ string, fallback time.Time) time.Time {
	return fallback
}
