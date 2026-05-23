package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type AnalyzerConfig struct {
	CPUErrorThreshold       float64 `json:"cpu_error_threshold" bson:"cpu_error_threshold"`
	CPURecoveryThreshold    float64 `json:"cpu_recovery_threshold" bson:"cpu_recovery_threshold"`
	MemoryErrorThreshold    float64 `json:"memory_error_threshold" bson:"memory_error_threshold"`
	MemoryRecoveryThreshold float64 `json:"memory_recovery_threshold" bson:"memory_recovery_threshold"`
}

type MetricPayload struct {
	TenantID    string  `json:"tenant_id"`
	HostID      string  `json:"host_id"`
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
}

type ConfigDocument struct {
	Component string         `bson:"component"`
	TenantID  string         `bson:"tenant_id"`
	Config    AnalyzerConfig `bson:"config"`
}

func TestE2EWatchSystem(t *testing.T) {
	ctx := context.Background()
	tenantID := "e2e-tenant"
	hostID := "e2e-host"
	stateKey := "state:hosts"
	fieldKey := fmt.Sprintf("%s:%s", tenantID, hostID)

	// ==========================================
	// 接続情報の初期化
	// ==========================================
	t.Log("MongoDB & Redis への接続初期化...")
	
	// MongoDB
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		t.Fatalf("MongoDB接続失敗: %v", err)
	}
	defer mongoClient.Disconnect(ctx)
	configCollection := mongoClient.Database("watch_system").Collection("configs")

	// Redis (テストホストから直接接続するため、ポートマッピングされた 6379 に接続)
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("Redis接続失敗: %v", err)
	}

	// ==========================================
	// テストデータのクリーンアップ
	// ==========================================
	t.Log("テスト前クリーンアップの実行...")
	_, _ = configCollection.DeleteMany(ctx, bson.M{"tenant_id": tenantID})
	_ = rdb.HDel(ctx, stateKey, fieldKey).Err()

	// ==========================================
	// ステップ1: 初期（デフォルト）設定の確認
	// ==========================================
	t.Log("ステップ1: デフォルト設定の取得検証...")
	resp, err := http.Get(fmt.Sprintf("http://localhost:8081/api/v1/config/analyzer/%s", tenantID))
	if err != nil {
		t.Fatalf("GET /config エラー: %v", err)
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期待ステータスは 200, 実際は: %d", resp.StatusCode)
	}
	
	var config AnalyzerConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		t.Fatalf("JSONデコード失敗: %v", err)
	}
	if config.CPUErrorThreshold != 80.0 {
		t.Fatalf("期待されるデフォルトCPUエラー閾値は 80.0, 実際は: %.1f", config.CPUErrorThreshold)
	}

	// ==========================================
	// ステップ2: 閾値設定の登録 (POST) と MongoDB 反映確認
	// ==========================================
	t.Log("ステップ2: 閾値設定の更新とMongoDB永続化検証...")
	newConfig := AnalyzerConfig{
		CPUErrorThreshold:       15.0,
		CPURecoveryThreshold:    10.0,
		MemoryErrorThreshold:    80.0,
		MemoryRecoveryThreshold: 70.0,
	}
	newConfigJSON, _ := json.Marshal(newConfig)
	
	postResp, err := http.Post(
		fmt.Sprintf("http://localhost:8081/api/v1/config/analyzer/%s", tenantID),
		"application/json",
		bytes.NewBuffer(newConfigJSON),
	)
	if err != nil {
		t.Fatalf("POST /config エラー: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("POST設定失敗: ステータス=%d", postResp.StatusCode)
	}
	
	// MongoDBを直接引いて確認
	var mongoDoc ConfigDocument
	err = configCollection.FindOne(ctx, bson.M{"tenant_id": tenantID, "component": "analyzer"}).Decode(&mongoDoc)
	if err != nil {
		t.Fatalf("MongoDBへの設定保存確認失敗: %v", err)
	}
	if mongoDoc.Config.CPUErrorThreshold != 15.0 {
		t.Fatalf("MongoDBの保存値が異なります: %.1f", mongoDoc.Config.CPUErrorThreshold)
	}

	// ==========================================
	// ステップ3: メトリクス送信 (POST) と Redis Queue 登録確認
	// ==========================================
	t.Log("ステップ3: メトリクス送信およびRedis Streams登録検証...")
	payload := MetricPayload{
		TenantID:    tenantID,
		HostID:      hostID,
		CPUUsage:    20.0, // CPUErrorThreshold (15.0) を超える値 (Alert想定)
		MemoryUsage: 50.0,
	}
	payloadJSON, _ := json.Marshal(payload)
	
	metricResp, err := http.Post(
		"http://localhost:8080/api/v1/metrics",
		"application/json",
		bytes.NewBuffer(payloadJSON),
	)
	if err != nil {
		t.Fatalf("POST /metrics エラー: %v", err)
	}
	defer metricResp.Body.Close()
	if metricResp.StatusCode != http.StatusOK {
		t.Fatalf("メトリクス送信失敗: ステータス=%d", metricResp.StatusCode)
	}

	// Redis Stream (metrics_stream) を確認
	streams, err := rdb.XRange(ctx, "metrics_stream", "-", "+").Result()
	if err != nil {
		t.Fatalf("Redis Stream 取得エラー: %v", err)
	}
	found := false
	for _, stream := range streams {
		dataStr, ok := stream.Values["data"].(string)
		if ok && bytes.Contains([]byte(dataStr), []byte(fmt.Sprintf(`"tenant_id":"%s"`, tenantID))) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("Redis Stream に該当テナントのメトリクスが登録されていません")
	}

	// ==========================================
	// ステップ4: Analyzer によるアラート遷移 (ALERT) の検証
	// ==========================================
	t.Log("ステップ4: Analyzer による ALERT 状態遷移検証...")
	
	// Analyzer コンテナが Pull して処理するのを待機 (最大5秒ポーリング)
	alertSuccess := false
	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)
		state, err := rdb.HGet(ctx, stateKey, fieldKey).Result()
		if err == nil && state == "Alert" {
			alertSuccess = true
			break
		}
	}
	if !alertSuccess {
		stateVal, _ := rdb.HGet(ctx, stateKey, fieldKey).Result()
		t.Fatalf("期待状態は 'Alert', 実際は: '%s'", stateVal)
	}
	t.Log("正常に ALERT 状態へ遷移しました")

	// ==========================================
	// ステップ5: 正常復帰 (RECOVERY) の検証
	// ==========================================
	t.Log("ステップ5: 通常状態への復帰(RECOVERY)検証...")
	recoveryPayload := MetricPayload{
		TenantID:    tenantID,
		HostID:      hostID,
		CPUUsage:    5.0, // CPURecoveryThreshold (10.0) 未満の値 (Recovery想定)
		MemoryUsage: 50.0,
	}
	recoveryJSON, _ := json.Marshal(recoveryPayload)
	
	recResp, err := http.Post(
		"http://localhost:8080/api/v1/metrics",
		"application/json",
		bytes.NewBuffer(recoveryJSON),
	)
	if err != nil {
		t.Fatalf("POST /metrics 復帰用データ送信エラー: %v", err)
	}
	defer recResp.Body.Close()

	// Analyzer の処理を待機 (最大5秒ポーリング)
	recoverySuccess := false
	for i := 0; i < 5; i++ {
		time.Sleep(1 * time.Second)
		state, err := rdb.HGet(ctx, stateKey, fieldKey).Result()
		if err == nil && state == "Normal" {
			recoverySuccess = true
			break
		}
	}
	if !recoverySuccess {
		stateVal, _ := rdb.HGet(ctx, stateKey, fieldKey).Result()
		t.Fatalf("期待状態は 'Normal', 実際は: '%s'", stateVal)
	}
	t.Log("正常に NORMAL 状態に復帰しました")

	// テストデータのクリーンアップ
	_, _ = configCollection.DeleteMany(ctx, bson.M{"tenant_id": tenantID})
	_ = rdb.HDel(ctx, stateKey, fieldKey).Err()
}

func TestE2EConfigValidation(t *testing.T) {
	tenantID := "e2e-validation-tenant"
	// 無効な設定を送り、Config Manager が正しく 400 Bad Request を返すか検証
	t.Log("E2E設定バリデーションテストの実行...")

	invalidConfig := AnalyzerConfig{
		CPUErrorThreshold:       10.0,
		CPURecoveryThreshold:    15.0, // エラー閾値より高い (無効)
		MemoryErrorThreshold:    80.0,
		MemoryRecoveryThreshold: 70.0,
	}
	invalidJSON, _ := json.Marshal(invalidConfig)

	resp, err := http.Post(
		fmt.Sprintf("http://localhost:8081/api/v1/config/analyzer/%s", tenantID),
		"application/json",
		bytes.NewBuffer(invalidJSON),
	)
	if err != nil {
		t.Fatalf("POST エラー: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("無効な設定に対して 400 を期待したが、結果は: %d", resp.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	t.Logf("バリデーションエラーメッセージ: %s", string(bodyBytes))
}
