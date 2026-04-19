package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
)

type MetricPayload struct {
	HostID      string  `json:"host_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	hostID, err := os.Hostname()
	if err != nil {
		hostID = "unknown-host"
	}

	consulAddr := getEnv("CONSUL_ADDR", "http://localhost:8500")
	intervalStr := getEnv("COLLECT_INTERVAL", "5s")
	interval, err := time.ParseDuration(intervalStr)

	if err != nil {
		interval = 5 * time.Second
	}

	log.Printf("エージェントを起動しました [Host: %s, Interval: %s]", hostID, interval)

	// 1. Consulクライアントの初期化
	consulConfig := api.DefaultConfig()
	consulConfig.Address = consulAddr
	consulClient, err := api.NewClient(consulConfig)
	if err != nil {
		log.Fatalf("Consulクライアントの初期化に失敗しました: %v", err)
	}

	// 永久ループで定期的にメトリクスを収集・送信
	for {
		payload := collectMetrics(hostID)

		// 2. サービスディスカバリ　(Consulに健全なAPIサーバの場所を聞く)
		apiURL, err := discoverAPIServer(consulClient)
		if err != nil {
			log.Printf("警告: APIサーバーが見つかりません (%v)", err)
			time.Sleep(interval)
			continue
		}

		// 3. APIサーバへ送信
		sendMetrics(apiURL, payload)

		time.Sleep(interval)
	}

}

// collectMetricsは CPUとメモリの使用率を取得します。
func collectMetrics(hostID string) MetricPayload {
	// CPU使用率を取得(1秒間の平均を計算するため、少しブロックします)
	cpuPercent, err := cpu.Percent(time.Second, false)
	var cpuUsage float64
	if err == nil && len(cpuPercent) > 0 {
		cpuUsage = cpuPercent[0]
	}

	// メモリ使用率を取得
	v, err := mem.VirtualMemory()
	var memUsage float64
	if err == nil {
		memUsage = v.UsedPercent
	}

	return MetricPayload{
		HostID:      hostID,
		CPUUsage:    cpuUsage,
		MemoryUsage: memUsage,
	}
}

// discoverAPIServer は Consul から API サーバーのアドレスを解決します
func discoverAPIServer(client *api.Client) (string, error) {
	// ヘルスチェックが「緑色(Passing)」になっているapiserverのみを取得
	services, _, err := client.Health().Service("apiserver", "", true, nil)
	if err != nil {
		return "", err
	}
	if len(services) == 0 {
		return "", fmt.Errorf("健全なAPIサーバーインスタンスが見つかりません")
	}

	// 今回は最初にみつかった健全なインスタンスを使用します
	service := services[0].Service
	url := fmt.Sprintf("http://%s:%d/api/v1/metrics", service.Address, service.Port)
	return url, nil
}

// sendMetrics は JSON ペイロードを HTTP POSTで送信します。
func sendMetrics(apiURL string, payload MetricPayload) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("JSONエンコードエラー: %v", err)
		return
	}

	resp, err := http.Post(apiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("データ送信エラー: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf("送信成功: CPU: %.1f%%, Memory: %.1f%% -> %s", payload.CPUUsage, payload.MemoryUsage, apiURL)
	} else {
		log.Printf("送信失敗: HTTPステータス %d", resp.StatusCode)
	}
}
