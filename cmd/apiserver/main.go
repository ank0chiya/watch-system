package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hashicorp/consul/api"
	"github.com/redis/go-redis/v9"
)

type MetricPayload struct {
	HostID      string  `json:"host_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
}

var rdb *redis.Client

// getEnv は環境変数を取得し、存在しない場合はフォールバックのデフォルト値を返します
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	// 環境変数から設定を読み込む（デフォルト値はローカル開発用）
	consulAddr := getEnv("CONSUL_ADDR", "127.0.0.1:8500")
	redisSentinelAddr := getEnv("REDIS_SENTINEL_ADDR", "127.0.0.1:26379")
	redisMasterName := getEnv("REDIS_MASTER_NAME", "mymaster")
	apiPort := getEnv("API_PORT", "8080")
	apiHost := getEnv("API_HOST", "127.0.0.1") // Consulに登録する自分自身のIP/ホスト名

	// ==========================================
	// 1. Consul へのサービス登録
	// ==========================================
	consulConfig := api.DefaultConfig()
	consulConfig.Address = consulAddr
	consulClient, err := api.NewClient(consulConfig)
	if err != nil {
		log.Fatalf("Consulクライアントの初期化に失敗しました: %v", err)
	}

	serviceID := "apiserver-1"
	registration := &api.AgentServiceRegistration{
		ID:      serviceID,
		Name:    "apiserver",
		Port:    8080,
		Address: apiHost,
		Check: &api.AgentServiceCheck{
			HTTP:     fmt.Sprintf("http://%s:%s/health", apiHost, apiPort),
			Interval: "10s",
			Timeout:  "5s",
		},
	}

	if err := consulClient.Agent().ServiceRegister(registration); err != nil {
		log.Fatalf("Consulへのサービス登録に失敗しました: %v", err)
	}
	log.Printf("Consulにサービスを登録しました: %s", serviceID)

	// プログラム終了時にConsulから登録を解除する（Graceful Shutdown）
	defer func() {
		consulClient.Agent().ServiceDeregister(serviceID)
		log.Println("Consulからサービスの登録を解除しました")
	}()

	// ==========================================
	// 2. Redis Sentinel への接続
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
	log.Println("Redis Sentinel経由での接続に成功しました")

	// ==========================================
	// 3. HTTPサーバーの起動
	// ==========================================
	http.HandleFunc("/api/v1/metrics", metricsHandler)
	http.HandleFunc("/health", healthHandler)

	server := &http.Server{
		Addr: ":" + apiPort,
	}

	go func() {
		log.Printf("APIサーバをポート%sで起動しました...", apiPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTPサーバーエラー: %v", err)
		}
	}()

	// 終了シグナル（Ctrl+Cなど）を待ち受ける
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("シャットダウン処理を開始します...")
	// ここでdeferで指定したConsulの登録解除処理が実行されます
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload MetricPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 構造体をJSON文字列に戻してRedisに保存する
	dataBytes, _ := json.Marshal(payload)

	// Redis Stream (metrics_stream)へ非同期キューイング
	ctx := context.Background()
	_, err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: "metrics_stream",
		Values: map[string]interface{}{
			"data": string(dataBytes),
		},
	}).Result()

	if err != nil {
		log.Printf("Redisへの書き込みに失敗しました： %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("メトリクスを受信・キューイングしました [Host: %s]", payload.HostID)
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}
