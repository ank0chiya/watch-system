package main

import (
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

// collectSystemMetrics は CPU とメモリの使用率を取得します
func collectSystemMetrics() (cpuUsage, memUsage float64) {
	// CPU使用率を取得 (1秒間の平均を計算するため、少しブロックします)
	cpuPercent, err := cpu.Percent(time.Second, false)
	if err == nil && len(cpuPercent) > 0 {
		cpuUsage = cpuPercent[0]
	}

	// メモリ使用率を取得
	v, err := mem.VirtualMemory()
	if err == nil {
		memUsage = v.UsedPercent
	}

	return cpuUsage, memUsage
}
