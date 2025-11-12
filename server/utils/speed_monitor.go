package utils

import (
	"fmt"
	"sync"
	"time"
)

type SpeedMonitor struct {
	startTime  time.Time
	lastTime   time.Time
	totalBytes int64
	lastBytes  int64
	clientAddr string
	mu         sync.Mutex
}

func NewSpeedMonitor(clientAddr string) *SpeedMonitor {
	now := time.Now()
	return &SpeedMonitor{
		startTime:  now,
		lastTime:   now,
		clientAddr: clientAddr,
	}
}

func (sm *SpeedMonitor) Update(bytes int64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.totalBytes += bytes
	sm.lastBytes += bytes
}

func (sm *SpeedMonitor) CheckAndPrint() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(sm.lastTime).Seconds()

	if elapsed >= 3.0 {
		instantSpeed := float64(sm.lastBytes) / elapsed
		averageSpeed := float64(sm.totalBytes) / now.Sub(sm.startTime).Seconds()

		fmt.Printf("[%s] Instant: %s, Average: %s, Total: %s\n",
			sm.clientAddr, formatSpeed(instantSpeed), formatSpeed(averageSpeed), formatBytes(sm.totalBytes))

		sm.lastTime = now
		sm.lastBytes = 0
		return true
	}
	return false
}

func formatSpeed(speed float64) string {
	switch {
	case speed >= 1024*1024:
		return fmt.Sprintf("%.2f MB/s", speed/(1024*1024))
	case speed >= 1024:
		return fmt.Sprintf("%.2f KB/s", speed/1024)
	default:
		return fmt.Sprintf("%.2f B/s", speed)
	}
}

func formatBytes(bytes int64) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
