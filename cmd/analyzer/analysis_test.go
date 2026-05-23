package main

import (
	"testing"
)

func TestEvaluateState(t *testing.T) {
	conf := AnalyzerConfig{
		CPUErrorThreshold:       80.0,
		CPURecoveryThreshold:    70.0,
		MemoryErrorThreshold:    80.0,
		MemoryRecoveryThreshold: 70.0,
		PingCount:               5,
		PingIntervalMS:          1000,
		PingTimeoutMS:           1000,
		CollectIntervalSeconds:  5,
		PingErrorLostThreshold:  3,
	}

	tests := []struct {
		name      string
		payload   MetricPayload
		lastState string
		expected  string
	}{
		{
			name: "All_Normal_Starts_Normal",
			payload: MetricPayload{
				CPUUsage:    50.0,
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 5},
				},
			},
			lastState: "Normal",
			expected:  "Normal",
		},
		{
			name: "CPU_Alert",
			payload: MetricPayload{
				CPUUsage:    85.0, // 閾値 80.0 超過
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 5},
				},
			},
			lastState: "Normal",
			expected:  "Alert",
		},
		{
			name: "Ping_Alert_Lost_3_Of_5",
			payload: MetricPayload{
				CPUUsage:    50.0,
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 2}, // ロスト数 3 >= 閾値 3
				},
			},
			lastState: "Normal",
			expected:  "Alert",
		},
		{
			name: "Ping_Normal_Lost_2_Of_5",
			payload: MetricPayload{
				CPUUsage:    50.0,
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 3}, // ロスト数 2 < 閾値 3
				},
			},
			lastState: "Normal",
			expected:  "Normal",
		},
		{
			name: "Hysteresis_Keep_Normal",
			payload: MetricPayload{
				CPUUsage:    75.0, // エラー 80.0 未満、復帰 70.0 以上 (中間域)
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 5},
				},
			},
			lastState: "Normal",
			expected:  "Normal",
		},
		{
			name: "Hysteresis_Keep_Alert",
			payload: MetricPayload{
				CPUUsage:    75.0, // エラー 80.0 未満、復帰 70.0 以上 (中間域)
				MemoryUsage: 50.0,
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 5},
				},
			},
			lastState: "Alert",
			expected:  "Alert",
		},
		{
			name: "Recovery_To_Normal",
			payload: MetricPayload{
				CPUUsage:    65.0, // 復帰基準 70.0 未満
				MemoryUsage: 60.0, // 復帰基準 70.0 未満
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 5},
				},
			},
			lastState: "Alert",
			expected:  "Normal",
		},
		{
			name: "No_Recovery_If_Ping_Still_Down",
			payload: MetricPayload{
				CPUUsage:    65.0, // 復帰基準 70.0 未満
				MemoryUsage: 60.0, // 復帰基準 70.0 未満
				PingResults: []PingResult{
					{Target: "host-1", Sent: 5, Received: 2}, // ロスト数 3 >= 閾値 3
				},
			},
			lastState: "Alert",
			expected:  "Alert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := evaluateState(tt.payload, conf, tt.lastState)
			if res != tt.expected {
				t.Errorf("期待値: %s, 実際は: %s", tt.expected, res)
			}
		})
	}
}
