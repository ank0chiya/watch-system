package main

import (
	"fmt"
	"os/exec"
	"time"
)

type PingResult struct {
	Target   string `json:"target"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
}

// pingHost は単一のパケットを送信して疎通を確認します
func pingHost(target string, timeoutMS int) bool {
	// タイムアウト時間（ミリ秒）を秒単位に変換（最低1秒）
	timeoutSecs := timeoutMS / 1000
	if timeoutSecs < 1 {
		timeoutSecs = 1
	}

	// -c 1: パケット数を1に制限
	// -W secs: タイムアウト秒数
	cmd := exec.Command("ping", "-c", "1", "-W", fmt.Sprintf("%d", timeoutSecs), target)
	err := cmd.Run()
	return err == nil
}

// collectPingMetrics は指定された全ターゲットに対して指定回数・間隔で疎通確認を実行します
func collectPingMetrics(targets []string, count, intervalMS, timeoutMS int) []PingResult {
	results := make([]PingResult, len(targets))

	for i, target := range targets {
		sent := 0
		received := 0

		for c := 0; c < count; c++ {
			if c > 0 {
				// 送信間隔の待機
				time.Sleep(time.Duration(intervalMS) * time.Millisecond)
			}
			sent++
			if pingHost(target, timeoutMS) {
				received++
			}
		}

		results[i] = PingResult{
			Target:   target,
			Sent:     sent,
			Received: received,
		}
	}

	return results
}
