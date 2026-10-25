//go:build !linux

package inventory

// PeakRSS is unavailable off Linux.
func PeakRSS() int64 { return 0 }
