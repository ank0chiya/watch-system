package main

import (
	"testing"
)

func TestPingHost_Localhost(t *testing.T) {
	// 127.0.0.1 への ping はローカル環境で常に成功するはず
	success := pingHost("127.0.0.1", 1000)
	if !success {
		t.Error("127.0.0.1 への ping に失敗しました")
	}
}

func TestPingHost_InvalidHost(t *testing.T) {
	// 存在しないIPへの ping は失敗するはず
	// 192.0.2.1 はドキュメント用の予約済みIPアドレス (RFC 5737) で疎通しないことが期待されます
	success := pingHost("192.0.2.1", 1000)
	if success {
		t.Error("存在しないIP 192.0.2.1 への ping が成功してしまいました")
	}
}

func TestCollectPingMetrics(t *testing.T) {
	targets := []string{"127.0.0.1"}
	// 2回送信、間隔 100ms、タイムアウト 1000ms
	results := collectPingMetrics(targets, 2, 100, 1000)

	if len(results) != 1 {
		t.Fatalf("結果の数が異なります: %d", len(results))
	}

	res := results[0]
	if res.Target != "127.0.0.1" {
		t.Errorf("ターゲット名が異なります: %s", res.Target)
	}
	if res.Sent != 2 {
		t.Errorf("期待送信数は 2, 実際は: %d", res.Sent)
	}
	if res.Received != 2 {
		t.Errorf("期待受信数は 2, 実際は: %d", res.Received)
	}
}
