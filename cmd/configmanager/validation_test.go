package main

import (
	"testing"
)

func TestValidateConfig_Valid(t *testing.T) {
	config := AnalyzerConfig{
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

	if err := validateConfig(config); err != nil {
		t.Errorf("正常な設定に対してエラーが発生しました: %v", err)
	}
}

func TestValidateConfig_InvalidThresholds(t *testing.T) {
	tests := []struct {
		name   string
		config AnalyzerConfig
	}{
		{
			name: "CPU_Error_Threshold_Too_High",
			config: AnalyzerConfig{
				CPUErrorThreshold:       101.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               5,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  3,
			},
		},
		{
			name: "CPU_Recovery_Equal_To_Error",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    80.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               5,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  3,
			},
		},
		{
			name: "Memory_Recovery_Greater_Than_Error",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 85.0,
				PingCount:               5,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  3,
			},
		},
		{
			name: "PingCount_Too_Small",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               0,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  3,
			},
		},
		{
			name: "LostThreshold_Greater_Than_PingCount",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               5,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  6, // 5回中6回ロストは矛盾
			},
		},
		{
			name: "PingInterval_Too_Small",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               5,
				PingIntervalMS:          50, // 50msは小さすぎる
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  5,
				PingErrorLostThreshold:  3,
			},
		},
		{
			name: "CollectInterval_Too_Small",
			config: AnalyzerConfig{
				CPUErrorThreshold:       80.0,
				CPURecoveryThreshold:    70.0,
				MemoryErrorThreshold:    80.0,
				MemoryRecoveryThreshold: 70.0,
				PingCount:               5,
				PingIntervalMS:          1000,
				PingTimeoutMS:           1000,
				CollectIntervalSeconds:  1, // 1秒は小さすぎる
				PingErrorLostThreshold:  3,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateConfig(tt.config); err == nil {
				t.Errorf("無効な設定に対してエラーが発生しませんでした: %s", tt.name)
			}
		})
	}
}
