# APIサーバー 動作確認ガイド

本ドキュメントは、コンテナ化されたAPIサーバー（`apiserver`）が正常に起動し、設計通りにConsulへのサービス登録、Redisへのキューイングを行えているかを確認するための手順である。

## 1. コンテナの起動状態確認

APIサーバーが正しくビルドされ、実行されているかを確認する。

### 1-1. コンテナステータスの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose ps apiserver
    ```
2. **期待結果:** `STATUS` が `Up` または `running` になっていること。

### 1-2. 起動ログの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose logs apiserver
    ```
2. **期待結果:** 以下のメッセージが順に出力されていること。
    - `Consulにサービスを登録しました: apiserver-1`
    - `Redis Sentinel経由での接続に成功しました`
    - `APIサーバーをポート...で起動しました...`

---

## 2. サービスディスカバリ（Consul）の確認

APIサーバーが自身の情報をConsulに正しく通知できているかを確認する。

### 2-1. Consul Web UI での確認

1. ブラウザで `http://localhost:8500` にアクセスする。
2. **期待結果:** `Services` 一覧に `apiserver` が存在し、ヘルスチェック（Health Checks）が緑色（Passing）になっていること。

### 2-2. HTTP API による確認

1. ターミナルで以下を実行：
    ```bash
    curl http://localhost:8500/v1/catalog/service/apiserver
    ```
2. **期待結果:** APIサーバーのIPアドレス（コンテナ内のIP）とポート（8080）を含むJSONデータが返却されること。

### 2-3. ヘルスチェックの異常検知（疑似障害テスト）

APIサーバーがフリーズした場合に、Consulが正しく異常（Timeout）を検知して対象から外すかを確認する。

1. ターミナルで以下を実行し、APIサーバーを一時停止（フリーズ）させる：
    ```bash
    docker pause apiserver
    ```
2. ブラウザでConsul Web UI (`http://localhost:8500`) を確認する。
    **期待結果:** 数秒（設定したTimeout時間）経過後、`apiserver` のヘルスチェックが失敗し、ステータスが赤色（Critical）に変わること。
3. ターミナルで以下を実行し、フリーズを解除する：
    ```bash
    docker unpause apiserver
    ```
    **期待結果:** 次のヘルスチェックのタイミングで、ステータスが再び緑色（Passing）に自動回復すること。

---

## 3. APIリクエストとデータフローの確認

エージェントからのデータ送信を模倣し、最終的にRedisのキューにデータが届くかを確認する。

### 3-1. テストデータのPOST送信

1. `curl` を使用して、ダミーのメトリクスデータを送信する：
    ```bash
    curl -X POST http://localhost:8080/api/v1/metrics \
         -H "Content-Type: application/json" \
         -d '{"host_id": "verify-host-01", "cpu_usage": 32.5, "memory_usage": 55.0}'
    ```
2. **期待結果:** レスポンスとして `OK` が返却されること。

### 3-2. Redis Streams (キュー) の確認

APIサーバーが受信したデータをRedisに正しく書き込めているかを確認する。

1. ターミナルで以下を実行し、Redis内のストリームデータを確認：
    ```bash
    docker exec -it redis-master redis-cli xrange metrics_stream - +
    ```
2. **期待結果:** `verify-host-01` を含むデータがID（タイムスタンプベース）と共にリスト表示されること。

---

## 4. トラブルシューティング

動作が期待通りでない場合は、以下の点を確認する。

- **名前解決エラー:** `apiserver` のログに `lookup consul: no such host` 等が出ている場合、`docker-compose.yml` の環境変数 `CONSUL_ADDR` が正しく設定されているか確認する。
- **Redis接続エラー:** Sentinelの設定（`REDIS_MASTER_NAME` 等）が `sentinel.conf` と一致しているか確認する。
- **再起動:** 設定変更後は以下のコマンドで再ビルドと再起動を行う：
    ```bash
    docker compose up -d --build apiserver
    ```