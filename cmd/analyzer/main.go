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

// MetricPayload は受信するメトリクス構造体です
type MetricPayload struct {
	TenantID    string  `json:"tenant_id"`
	HostID      string  `json:"host_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
}

// AnalyzerConfig は閾値設定構造体です
type AnalyzerConfig struct {
	CPUErrorThreshold       float64 `json:"cpu_error_threshold"`
	CPURecoveryThreshold    float64 `json:"cpu_recovery_threshold"`
	MemoryErrorThreshold    float64 `json:"memory_error_threshold"`
	MemoryRecoveryThreshold float64 `json:"memory_recovery_threshold"`
}

// CachedConfig はキャッシュ用の構造体です
type CachedConfig struct {
	Config    AnalyzerConfig
	FetchedAt time.Time
}

// Notifier は通知を行うインターフェースです
type Notifier interface {
	NotifyAlert(tenantID, hostID string, cpu, mem float64, conf AnalyzerConfig)
	NotifyRecovery(tenantID, hostID string, cpu, mem float64, conf AnalyzerConfig)
}

// LogNotifier はコンソールに通知を出力する実装です
type LogNotifier struct{}

func (n *LogNotifier) NotifyAlert(tenantID, hostID string, cpu, mem float64, conf AnalyzerConfig) {
	log.Printf(`
**************************************************
[ALERT] 異常状態を検知しました！
- テナント: %s
- ホスト: %s
- CPU使用率: %.1f%% (閾値: %.1f%%)
- メモリ使用率: %.1f%% (閾値: %.1f%%)
- 検知時刻: %s
**************************************************`,
		tenantID, hostID, cpu, conf.CPUErrorThreshold, mem, conf.MemoryErrorThreshold, time.Now().Format("2006-01-02 15:04:05"))
}

func (n *LogNotifier) NotifyRecovery(tenantID, hostID string, cpu, mem float64, conf AnalyzerConfig) {
	log.Printf(`
**************************************************
[RECOVERY] 通常状態に復帰しました。
- テナント: %s
- ホスト: %s
- CPU使用率: %.1f%% (復帰基準: < %.1f%%)
- メモリ使用率: %.1f%% (復帰基準: < %.1f%%)
- 復帰時刻: %s
**************************************************`,
		tenantID, hostID, cpu, conf.CPURecoveryThreshold, mem, conf.MemoryRecoveryThreshold, time.Now().Format("2006-01-02 15:04:05"))
}

var (
	rdb             *redis.Client
	consulClient    *api.Client
	configCache     = make(map[string]CachedConfig)
	configCacheMu   sync.RWMutex
	cacheTTL        = 10 * time.Second
	defaultConfig   = AnalyzerConfig{
		CPUErrorThreshold:       80.0,
		CPURecoveryThreshold:    70.0,
		MemoryErrorThreshold:    80.0,
		MemoryRecoveryThreshold: 70.0,
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
	// XGroupCreateMkStream は Stream 自体がなくても自動作成します
	err = rdb.XGroupCreateMkStream(context.Background(), "metrics_stream", "metrics_group", "0").Err()
	if err != nil {
		// すでにグループが存在する場合は BUSYGROUP エラーが返るので無視する
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
			// Redis Stream からデータを Pull (2秒ブロック)
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
					time.Sleep(1 * time.Second) // エラー時は少し待機
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
	
	// 初期状態は "Normal" とする
	lastState, err := rdb.HGet(ctx, stateKey, fieldKey).Result()
	if err == redis.Nil {
		lastState = "Normal"
	} else if err != nil {
		log.Printf("Redisからの前回の状態の取得エラー: %v", err)
		return
	}

	// 3. 状態の判定 (ヒステリシスロジック)
	newState := lastState

	// エラー判定: CPU または メモリがエラー閾値以上
	isError := payload.CPUUsage >= conf.CPUErrorThreshold || payload.MemoryUsage >= conf.MemoryErrorThreshold
	// 復帰判定: CPU と メモリが両方リカバリ閾値未満
	isRecovery := payload.CPUUsage < conf.CPURecoveryThreshold && payload.MemoryUsage < conf.MemoryRecoveryThreshold

	if isError {
		newState = "Alert"
	} else if isRecovery {
		newState = "Normal"
	}
	// 中間域の場合は newState = lastState のまま（変化なし）

	// 4. 状態遷移の検知および通知
	if newState != lastState {
		if newState == "Alert" {
			notifier.NotifyAlert(payload.TenantID, payload.HostID, payload.CPUUsage, payload.MemoryUsage, conf)
		} else if newState == "Normal" {
			notifier.NotifyRecovery(payload.TenantID, payload.HostID, payload.CPUUsage, payload.MemoryUsage, conf)
		}

		// Redis の状態を更新
		if err := rdb.HSet(ctx, stateKey, fieldKey, newState).Err(); err != nil {
			log.Printf("Redisの状態更新エラー: %v", err)
		}
	}

	// 処理完了 (ACK)
	rdb.XAck(ctx, "metrics_stream", "metrics_group", msg.ID)
}

func getTenantConfig(tenantID string) AnalyzerConfig {
	if tenantID == "" {
		return defaultConfig
	}
	configCacheMu.RLock()
	cached, found := configCache[tenantID]
	configCacheMu.RUnlock()

	// キャッシュが有効な場合はそのまま返す
	if found && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Config
	}

	// キャッシュが無効または見つからない場合は Config Manager から取得を試みる
	configCacheMu.Lock()
	defer configCacheMu.Unlock()

	// Lock獲得の間に他スレッドが更新した可能性を再確認
	cached, found = configCache[tenantID]
	if found && time.Since(cached.FetchedAt) < cacheTTL {
		return cached.Config
	}

	config, err := fetchConfigFromManager(tenantID)
	if err != nil {
		log.Printf("警告: Config Manager からの設定取得に失敗しました (デフォルト設定を適用します): %v", err)
		// 失敗した場合はキャッシュにデフォルトを入れつつ、エラーリトライのため短いFetchedAtを設定する
		config = defaultConfig
	}

	configCache[tenantID] = CachedConfig{
		Config:    config,
		FetchedAt: time.Now(),
	}

	return config
}

func fetchConfigFromManager(tenantID string) (AnalyzerConfig, error) {
	// Consulからconfigmanagerサービスのアドレスを解決
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

	return conf, nil
}
