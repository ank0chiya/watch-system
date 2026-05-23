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
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AnalyzerConfig は Analyzer の設定構造体です
type AnalyzerConfig struct {
	CPUErrorThreshold       float64 `json:"cpu_error_threshold" bson:"cpu_error_threshold"`
	CPURecoveryThreshold    float64 `json:"cpu_recovery_threshold" bson:"cpu_recovery_threshold"`
	MemoryErrorThreshold    float64 `json:"memory_error_threshold" bson:"memory_error_threshold"`
	MemoryRecoveryThreshold float64 `json:"memory_recovery_threshold" bson:"memory_recovery_threshold"`
}

// ConfigDocument は MongoDB に保存するドキュメント構造体です
type ConfigDocument struct {
	Component string         `bson:"component"`
	TenantID  string         `bson:"tenant_id"`
	Config    AnalyzerConfig `bson:"config"`
	UpdatedAt time.Time      `bson:"updated_at"`
}

var mongoClient *mongo.Client
var configCollection *mongo.Collection

func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func main() {
	consulAddr := getEnv("CONSUL_ADDR", "127.0.0.1:8500")
	mongoURI := getEnv("MONGO_URI", "mongodb://127.0.0.1:27017")
	apiPort := getEnv("CONFIG_MANAGER_PORT", "8081")
	apiHost := getEnv("CONFIG_MANAGER_HOST", "127.0.0.1")

	// ==========================================
	// 1. MongoDB への接続
	// ==========================================
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clientOptions := options.Client().ApplyURI(mongoURI)
	var err error
	mongoClient, err = mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatalf("MongoDB 接続に失敗しました: %v", err)
	}

	err = mongoClient.Ping(ctx, nil)
	if err != nil {
		log.Fatalf("MongoDB への Ping に失敗しました: %v", err)
	}
	log.Println("MongoDB への接続に成功しました")

	configCollection = mongoClient.Database("watch_system").Collection("configs")

	// データのインデックス作成 (component, tenant_id のユニークインデックス)
	_, err = configCollection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "component", Value: 1}, {Key: "tenant_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		log.Printf("警告: インデックス作成に失敗しました: %v", err)
	}

	// ==========================================
	// 2. Consul へのサービス登録
	// ==========================================
	consulConfig := api.DefaultConfig()
	consulConfig.Address = consulAddr
	consulClient, err := api.NewClient(consulConfig)
	if err != nil {
		log.Fatalf("Consulクライアントの初期化に失敗しました: %v", err)
	}

	serviceID := "configmanager-1"
	portVal := 8081
	fmt.Sscanf(apiPort, "%d", &portVal)

	registration := &api.AgentServiceRegistration{
		ID:      serviceID,
		Name:    "configmanager",
		Port:    portVal,
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

	defer func() {
		consulClient.Agent().ServiceDeregister(serviceID)
		log.Println("Consulからサービスの登録を解除しました")
		if err := mongoClient.Disconnect(context.Background()); err != nil {
			log.Printf("MongoDBの切断エラー: %v", err)
		}
	}()

	// ==========================================
	// 3. HTTPサーバーの起動 (Go 1.22+ ルーティング)
	// ==========================================
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/config/analyzer/{tenant_id}", handleGetConfig)
	mux.HandleFunc("POST /api/v1/config/analyzer/{tenant_id}", handlePostConfig)
	mux.HandleFunc("GET /health", handleHealth)

	server := &http.Server{
		Addr:    ":" + apiPort,
		Handler: mux,
	}

	go func() {
		log.Printf("Config Managerをポート%sで起動しました...", apiPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTPサーバーエラー: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("シャットダウン処理を開始します...")
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}

// デフォルト設定を返します。
func getDefaultConfig() AnalyzerConfig {
	return AnalyzerConfig{
		CPUErrorThreshold:       80.0,
		CPURecoveryThreshold:    70.0,
		MemoryErrorThreshold:    80.0,
		MemoryRecoveryThreshold: 70.0,
	}
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	if tenantID == "" {
		http.Error(w, "Missing tenant_id", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var doc ConfigDocument
	filter := bson.M{"component": "analyzer", "tenant_id": tenantID}
	err := configCollection.FindOne(ctx, filter).Decode(&doc)

	if err == mongo.ErrNoDocuments {
		// 未登録の場合はデフォルト値を返す
		defaultConf := getDefaultConfig()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(defaultConf)
		return
	} else if err != nil {
		log.Printf("DB取得エラー: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc.Config)
}

func handlePostConfig(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenant_id")
	if tenantID == "" {
		http.Error(w, "Missing tenant_id", http.StatusBadRequest)
		return
	}

	var reqConfig AnalyzerConfig
	if err := json.NewDecoder(r.Body).Decode(&reqConfig); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 簡易バリデーション
	if reqConfig.CPUErrorThreshold <= 0 || reqConfig.CPUErrorThreshold > 100 ||
		reqConfig.CPURecoveryThreshold <= 0 || reqConfig.CPURecoveryThreshold > 100 ||
		reqConfig.MemoryErrorThreshold <= 0 || reqConfig.MemoryErrorThreshold > 100 ||
		reqConfig.MemoryRecoveryThreshold <= 0 || reqConfig.MemoryRecoveryThreshold > 100 {
		http.Error(w, "Thresholds must be between 0 and 100", http.StatusBadRequest)
		return
	}
	// リカバリ閾値がエラー閾値より高い、または等しい場合はエラーにする（ヒステリシスの整合性）
	if reqConfig.CPURecoveryThreshold >= reqConfig.CPUErrorThreshold {
		http.Error(w, "CPU recovery threshold must be less than error threshold", http.StatusBadRequest)
		return
	}
	if reqConfig.MemoryRecoveryThreshold >= reqConfig.MemoryErrorThreshold {
		http.Error(w, "Memory recovery threshold must be less than error threshold", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	filter := bson.M{"component": "analyzer", "tenant_id": tenantID}
	update := bson.M{
		"$set": bson.M{
			"config":     reqConfig,
			"updated_at": time.Now(),
		},
	}
	opts := options.Update().SetUpsert(true)

	_, err := configCollection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		log.Printf("DB書き込みエラー: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	log.Printf("設定を更新しました [Tenant: %s, CPU: %.1f%%/%.1f%%, Mem: %.1f%%/%.1f%%]",
		tenantID,
		reqConfig.CPUErrorThreshold, reqConfig.CPURecoveryThreshold,
		reqConfig.MemoryErrorThreshold, reqConfig.MemoryRecoveryThreshold)

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "OK")
}
