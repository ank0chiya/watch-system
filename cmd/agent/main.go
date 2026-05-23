package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/consul/api"
)

type MetricPayload struct {
	TenantID    string       `json:"tenant_id"`
	HostID      string       `json:"host_id"`
	CPUUsage    float64      `json:"cpu_usage"`
	MemoryUsage float64      `json:"memory_usage"`
	PingResults []PingResult `json:"ping_results"`
}

type AgentConfig struct {
	PingCount              int `json:"ping_count"`
	PingIntervalMS         int `json:"ping_interval_ms"`
	PingTimeoutMS          int `json:"ping_timeout_ms"`
	CollectIntervalSeconds int `json:"collect_interval_seconds"`
}

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func getDefaultAgentConfig() AgentConfig {
	return AgentConfig{
		PingCount:              5,
		PingIntervalMS:         1000,
		PingTimeoutMS:          1000,
		CollectIntervalSeconds: 5,
	}
}

func main() {
	hostID, err := os.Hostname()
	if err != nil {
		hostID = "unknown-host"
	}

	consulAddr := getEnv("CONSUL_ADDR", "http://localhost:8500")
	tenantID := getEnv("TENANT_ID", "default-tenant")

	// PING_TARGETSの読み込み（カンマ区切り）
	pingTargetsStr := getEnv("PING_TARGETS", "")
	var pingTargets []string
	if pingTargetsStr != "" {
		for _, t := range strings.Split(pingTargetsStr, ",") {
			trimmed := strings.TrimSpace(t)
			if trimmed != "" {
				pingTargets = append(pingTargets, trimmed)
			}
		}
	}

	log.Printf("エージェントを起動しました [Tenant: %s, Host: %s, Targets: %v]", tenantID, hostID, pingTargets)

	// 1. Consulクライアントの初期化
	consulConfig := api.DefaultConfig()
	consulConfig.Address = consulAddr
	consulClient, err := api.NewClient(consulConfig)
	if err != nil {
		log.Fatalf("Consulクライアントの初期化に失敗しました: %v", err)
	}

	var currentConfig AgentConfig
	var lastConfigSync time.Time

	// 永久ループで定期的にメトリクスを収集・送信
	for {
		// 1. 設定の同期 (初回、または前回の同期から30秒以上経過している場合)
		if lastConfigSync.IsZero() || time.Since(lastConfigSync) > 30*time.Second {
			currentConfig = fetchConfigFromManager(consulClient, tenantID)
			lastConfigSync = time.Now()
			log.Printf("設定を同期しました: [Count: %d, Interval: %dms, Timeout: %dms, CollectInterval: %ds]",
				currentConfig.PingCount, currentConfig.PingIntervalMS, currentConfig.PingTimeoutMS, currentConfig.CollectIntervalSeconds)
		}

		// 2. システムリソース収集 (system.go から呼出)
		cpuUsage, memUsage := collectSystemMetrics()

		// 3. Ping測定 (ping.go から呼出)
		var pingResults []PingResult
		if len(pingTargets) > 0 {
			pingResults = collectPingMetrics(pingTargets, currentConfig.PingCount, currentConfig.PingIntervalMS, currentConfig.PingTimeoutMS)
		}

		payload := MetricPayload{
			TenantID:    tenantID,
			HostID:      hostID,
			CPUUsage:    cpuUsage,
			MemoryUsage: memUsage,
			PingResults: pingResults,
		}

		// 4. サービスディスカバリ　(Consulに健全なAPIサーバの場所を聞く)
		apiURL, err := discoverAPIServer(consulClient)
		if err != nil {
			log.Printf("警告: APIサーバーが見つかりません (%v)", err)
		} else {
			// 5. APIサーバへ送信
			sendMetrics(apiURL, payload)
		}

		// 設定された間隔で待機
		time.Sleep(time.Duration(currentConfig.CollectIntervalSeconds) * time.Second)
	}
}

func mergeWithDefaults(c AgentConfig) AgentConfig {
	defaultConf := getDefaultAgentConfig()
	if c.PingCount == 0 {
		c.PingCount = defaultConf.PingCount
	}
	if c.PingIntervalMS == 0 {
		c.PingIntervalMS = defaultConf.PingIntervalMS
	}
	if c.PingTimeoutMS == 0 {
		c.PingTimeoutMS = defaultConf.PingTimeoutMS
	}
	if c.CollectIntervalSeconds == 0 {
		c.CollectIntervalSeconds = defaultConf.CollectIntervalSeconds
	}
	return c
}

// fetchConfigFromManager は Config Manager から設定を同期します
func fetchConfigFromManager(consulClient *api.Client, tenantID string) AgentConfig {
	services, _, err := consulClient.Health().Service("configmanager", "", true, nil)
	if err != nil || len(services) == 0 {
		return getDefaultAgentConfig()
	}

	service := services[0].Service
	url := fmt.Sprintf("http://%s:%d/api/v1/config/analyzer/%s", service.Address, service.Port, tenantID)

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("警告: Config Manager への接続に失敗しました: %v", err)
		return getDefaultAgentConfig()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return getDefaultAgentConfig()
	}

	var conf AgentConfig
	if err := json.NewDecoder(resp.Body).Decode(&conf); err != nil {
		return getDefaultAgentConfig()
	}

	return mergeWithDefaults(conf)
}

// discoverAPIServer は Consul から API サーバーのアドレスを解決します
func discoverAPIServer(client *api.Client) (string, error) {
	services, _, err := client.Health().Service("apiserver", "", true, nil)
	if err != nil {
		return "", err
	}
	if len(services) == 0 {
		return "", fmt.Errorf("健全なAPIサーバーインスタンスが見つかりません")
	}

	service := services[0].Service
	url := fmt.Sprintf("http://%s:%d/api/v1/metrics", service.Address, service.Port)
	return url, nil
}

// sendMetrics は JSON ペイロードを HTTP POSTで送信します
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
		log.Printf("送信成功: CPU: %.1f%%, Memory: %.1f%%, Pings: %d -> %s",
			payload.CPUUsage, payload.MemoryUsage, len(payload.PingResults), apiURL)
	} else {
		log.Printf("送信失敗: HTTPステータス %d", resp.StatusCode)
	}
}
