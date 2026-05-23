package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/redis/go-redis/v9"
)

type PingResult struct {
	Target   string `json:"target"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
}

// MetricPayload は受信するメトリクス構造体です
type MetricPayload struct {
	TenantID    string       `json:"tenant_id"`
	HostID      string       `json:"host_id"`
	CPUUsage    float64      `json:"cpu_usage"`
	MemoryUsage float64      `json:"memory_usage"`
	PingResults []PingResult `json:"ping_results"`
}

// AnalyzerConfig は閾値設定構造体です
type AnalyzerConfig struct {
	CPUErrorThreshold       float64 `json:"cpu_error_threshold"`
	CPURecoveryThreshold    float64 `json:"cpu_recovery_threshold"`
	MemoryErrorThreshold    float64 `json:"memory_error_threshold"`
	MemoryRecoveryThreshold float64 `json:"memory_recovery_threshold"`
	PingCount               int     `json:"ping_count"`
	PingIntervalMS          int     `json:"ping_interval_ms"`
	PingTimeoutMS           int     `json:"ping_timeout_ms"`
	CollectIntervalSeconds  int     `json:"collect_interval_seconds"`
	PingErrorLostThreshold  int     `json:"ping_error_lost_threshold"`
}

// CachedConfig はキャッシュ用の構造体です
type CachedConfig struct {
	Config    AnalyzerConfig
	FetchedAt time.Time
}

// Notifier は通知を行うインターフェースです
type Notifier interface {
	NotifyAlert(tenantID, hostID string, payload MetricPayload, conf AnalyzerConfig)
	NotifyRecovery(tenantID, hostID string, payload MetricPayload, conf AnalyzerConfig)
}

// LogNotifier はコンソールに通知を出力する実装です
type LogNotifier struct{}

func (n *LogNotifier) NotifyAlert(tenantID, hostID string, payload MetricPayload, conf AnalyzerConfig) {
	pingDetails := ""
	for _, p := range payload.PingResults {
		pingDetails += fmt.Sprintf("\n- Ping Target: %s, Sent: %d, Received: %d (Lost: %d, Threshold: %d)",
			p.Target, p.Sent, p.Received, p.Sent-p.Received, conf.PingErrorLostThreshold)
	}

	log.Printf(`
**************************************************
[ALERT] 異常状態を検知しました！
- テナント: %s
- ホスト: %s
- CPU使用率: %.1f%% (閾値: %.1f%%)
- メモリ使用率: %.1f%% (閾値: %.1f%%)%s
- 検知時刻: %s
**************************************************`,
		tenantID, hostID, payload.CPUUsage, conf.CPUErrorThreshold, payload.MemoryUsage, conf.MemoryErrorThreshold,
		pingDetails, time.Now().Format("2006-01-02 15:04:05"))
}

func (n *LogNotifier) NotifyRecovery(tenantID, hostID string, payload MetricPayload, conf AnalyzerConfig) {
	pingDetails := ""
	for _, p := range payload.PingResults {
		pingDetails += fmt.Sprintf("\n- Ping Target: %s, Sent: %d, Received: %d (Lost: %d, Threshold: %d)",
			p.Target, p.Sent, p.Received, p.Sent-p.Received, conf.PingErrorLostThreshold)
	}

	log.Printf(`
**************************************************
[RECOVERY] 通常状態に復帰しました。
- テナント: %s
- ホスト: %s
- CPU使用率: %.1f%% (復帰基準: < %.1f%%)
- メモリ使用率: %.1f%% (復帰基準: < %.1f%%)%s
- 復帰時刻: %s
**************************************************`,
		tenantID, hostID, payload.CPUUsage, conf.CPURecoveryThreshold, payload.MemoryUsage, conf.MemoryRecoveryThreshold,
		pingDetails, time.Now().Format("2006-01-02 15:04:05"))
}

var (
	rdb           *redis.Client
	consulClient  *api.Client
	configCache   = make(map[string]CachedConfig)
	configCacheMu sync.RWMutex
	cacheTTL      = 10 * time.Second
	defaultConfig = AnalyzerConfig{
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
)

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	consulAddr := getEnv("CONSUL_ADDR", "127.0.0.1:8500")
	redisSentinelAddr := getEnv("REDIS_SENTINEL_ADDR", "127.0.0.1:26379")
	redisMasterName := getEnv("REDIS_MASTER_NAME", "mymaster")

	// ==========================================
	// 1. Consul クライアントの初期化
	// ==========================================
	consulConfig := api.DefaultConfig()
	consulConfig.Address = consulAddr
	var err error
	consulClient, err = api.NewClient(consulConfig)
	if err != nil {
		log.Fatalf("Consulクライアントの初期化に失敗しました: %v", err)
	}
	log.Println("Consulクライアントの初期化に成功しました")

	// ==========================================
	// 2. Redis Sentinel 接続
	// ==========================================
	rdb = redis.NewFailoverClient(&redis.FailoverOptions{
		MasterName:    redisMasterName,
		SentinelAddrs: []string{redisSentinelAddr},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis への接続に失敗しました: %v", err)
	}
	log.Println("Redis Sentinel接続に成功しました")

	// Redis Stream グループ作成 (存在しない場合は作成)
	err = rdb.XGroupCreateMkStream(context.Background(), "metrics_stream", "metrics_group", "0").Err()
	if err != nil {
		log.Printf("XGroupCreateMkStream 情報 (エラーではない可能性があります): %v", err)
	}

	// ==========================================
	// 3. 解析ループの開始
	// ==========================================
	notifier := &LogNotifier{}
	stopChan := make(chan struct{})

	go startAnalysisLoop(notifier, stopChan)

	// 終了シグナルの待機
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("シャットダウン処理を開始します...")
	close(stopChan)
	rdb.Close()
	log.Println("Analyzerを停止しました")
}

func startAnalysisLoop(notifier Notifier, stopChan chan struct{}) {
	log.Println("解析ループを開始しました...")
	consumerName := "analyzer_consumer"

	for {
		select {
		case <-stopChan:
			return
		default:
			ctx := context.Background()
			streams, err := rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
				Group:    "metrics_group",
				Consumer: consumerName,
				Streams:  []string{"metrics_stream", ">"},
				Count:    10,
				Block:    2 * time.Second,
			}).Result()

			if err != nil {
				if err != redis.Nil {
					log.Printf("Redis Streamの読み取りエラー: %v", err)
					time.Sleep(1 * time.Second)
				}
				continue
			}

			for _, stream := range streams {
				for _, msg := range stream.Messages {
					processMessage(ctx, msg, notifier)
				}
			}
		}
	}
}

func processMessage(ctx context.Context, msg redis.XMessage, notifier Notifier) {
	dataStr, ok := msg.Values["data"].(string)
	if !ok {
		log.Printf("警告: メッセージに data フィールドが存在しないか文字列ではありません: %v", msg.ID)
		rdb.XAck(ctx, "metrics_stream", "metrics_group", msg.ID)
		return
	}

	var payload MetricPayload
	if err := json.Unmarshal([]byte(dataStr), &payload); err != nil {
		log.Printf("警告: JSONデコードエラー: %v", err)
		rdb.XAck(ctx, "metrics_stream", "metrics_group", msg.ID)
		return
	}

	// 1. テナント別の設定取得
	conf := getTenantConfig(payload.TenantID)

	// 2. Redis から前回の状態を取得
	stateKey := "state:hosts"
	fieldKey := fmt.Sprintf("%s:%s", payload.TenantID, payload.HostID)
	
	lastState, err := rdb.HGet(ctx, stateKey, fieldKey).Result()
	if err == redis.Nil {
		lastState = "Normal"
	} else if err != nil {
		log.Printf("Redisからの前回の状態の取得エラー: %v", err)
		return
	}

	// 3. 状態の判定 (evaluateState の呼び出し)
	newState := evaluateState(payload, conf, lastState)

	// 4. 状態遷移の検知および通知
	if newState != lastState {
		if newState == "Alert" {
			notifier.NotifyAlert(payload.TenantID, payload.HostID, payload, conf)
		} else if newState == "Normal" {
			notifier.NotifyRecovery(payload.TenantID, payload.HostID, payload, conf)
		}

		// Redis の状態を更新
		if err := rdb.HSet(ctx, stateKey, fieldKey, newState).Err(); err != nil {
			log.Printf("Redisの状態更新エラー: %v", err)
		}
	}

	// 処理完了 (ACK)
	rdb.XAck(ctx, "metrics_stream", "metrics_group", msg.ID)
}

// evaluateState は、現在のメトリクス、設定値、および前回状態をもとに、新しい状態を評価します (単体テスト可能)
func evaluateState(payload MetricPayload, conf AnalyzerConfig, lastState string) string {
	// 1. リソースエラー判定
	isResourceError := payload.CPUUsage >= conf.CPUErrorThreshold || payload.MemoryUsage >= conf.MemoryErrorThreshold

	// 2. Pingエラー判定 (しきい値以上のパケットロストが1つでもあるか)
	isPingError := false
	for _, p := range payload.PingResults {
		lost := p.Sent - p.Received
		if lost >= conf.PingErrorLostThreshold {
			isPingError = true
			break
		}
	}

	isError := isResourceError || isPingError

	// 3. 復帰判定 (リソースがすべて復帰値未満かつ、すべてのPingのロスト数がしきい値未満)
	isResourceRecovery := payload.CPUUsage < conf.CPURecoveryThreshold && payload.MemoryUsage < conf.MemoryRecoveryThreshold
	isPingRecovery := true
	for _, p := range payload.PingResults {
		lost := p.Sent - p.Received
		if lost >= conf.PingErrorLostThreshold {
			isPingRecovery = false
			break
		}
	}

	isRecovery := isResourceRecovery && isPingRecovery

	if isError {
		return "Alert"
	} else if isRecovery {
		return "Normal"
	}

	// 中間領域にいる場合は状態維持
	return lastState
}

func getTenantConfig(tenantID string) AnalyzerConfig {
	if tenantID == "" {
		return defaultConfig
	}
	configCacheMu.RLock()
	cached, found := configCache[tenantID]
	configCacheMu.RUnlock()

	if found && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Config
	}

	configCacheMu.Lock()
	defer configCacheMu.Unlock()

	cached, found = configCache[tenantID]
	if found && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Config
	}

	config, err := fetchConfigFromManager(tenantID)
	if err != nil {
		log.Printf("警告: Config Manager からの設定取得に失敗しました (デフォルト設定を適用します): %v", err)
		config = defaultConfig
	}

	configCache[tenantID] = CachedConfig{
		Config:    config,
		FetchedAt: time.Now(),
	}

	return config
}

func mergeWithDefaults(c AnalyzerConfig) AnalyzerConfig {
	if c.CPUErrorThreshold == 0 {
		c.CPUErrorThreshold = defaultConfig.CPUErrorThreshold
	}
	if c.CPURecoveryThreshold == 0 {
		c.CPURecoveryThreshold = defaultConfig.CPURecoveryThreshold
	}
	if c.MemoryErrorThreshold == 0 {
		c.MemoryErrorThreshold = defaultConfig.MemoryErrorThreshold
	}
	if c.MemoryRecoveryThreshold == 0 {
		c.MemoryRecoveryThreshold = defaultConfig.MemoryRecoveryThreshold
	}
	if c.PingCount == 0 {
		c.PingCount = defaultConfig.PingCount
	}
	if c.PingIntervalMS == 0 {
		c.PingIntervalMS = defaultConfig.PingIntervalMS
	}
	if c.PingTimeoutMS == 0 {
		c.PingTimeoutMS = defaultConfig.PingTimeoutMS
	}
	if c.CollectIntervalSeconds == 0 {
		c.CollectIntervalSeconds = defaultConfig.CollectIntervalSeconds
	}
	if c.PingErrorLostThreshold == 0 {
		c.PingErrorLostThreshold = defaultConfig.PingErrorLostThreshold
	}
	return c
}

func fetchConfigFromManager(tenantID string) (AnalyzerConfig, error) {
	services, _, err := consulClient.Health().Service("configmanager", "", true, nil)
	if err != nil || len(services) == 0 {
		return defaultConfig, fmt.Errorf("健全な configmanager サービスが見つかりません: %v", err)
	}

	service := services[0].Service
	url := fmt.Sprintf("http://%s:%d/api/v1/config/analyzer/%s", service.Address, service.Port, tenantID)

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return defaultConfig, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return defaultConfig, fmt.Errorf("Config Manager からエラーレスポンスが返されました (URL: %s): %d", url, resp.StatusCode)
	}

	var conf AnalyzerConfig
	if err := json.NewDecoder(resp.Body).Decode(&conf); err != nil {
		return defaultConfig, err
	}

	return mergeWithDefaults(conf), nil
}
