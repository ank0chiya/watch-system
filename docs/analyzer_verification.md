# Analyzer 動作確認ガイド

本ドキュメントは、コンテナ化された解析・通知サービス（`analyzer`）が正常に起動し、Redis Streams からのデータ受信、設定取得、閾値判定、および状態遷移（ALERT/RECOVERY）のログ通知が正しく機能しているかを確認するための手順である。

---

## 1. コンテナの起動状態確認

Analyzer が正しく実行されているかを確認する。

### 1-1. コンテナステータスの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose ps analyzer
    ```
2. **期待結果:** `STATUS` が `Up` または `running` になっていること。

### 1-2. 起動ログの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose logs analyzer
    ```
2. **期待結果:** 以下のメッセージが順に出力されていること。
    - `Consulクライアントの初期化に成功しました`
    - `Redis Sentinel接続に成功しました`
    - `解析ループを開始しました...`

---

## 2. 解析と状態遷移（アラート/復帰）の動作確認

エージェントから送信されるデータに対し、登録した閾値に基づく状態遷移とログ通知（模擬アラート）を確認する。

### 2-1. アラート (ALERT) の発生テスト

通常のエージェントのCPU使用率（2%〜10%程度）でアラートが発生するように、一時的に低い閾値を設定して検証する。

1. `tenant-1` 用のCPUエラー閾値を `2.0%` に引き下げる：
    ```bash
    curl -X POST -H "Content-Type: application/json" \
         -d '{"cpu_error_threshold":2.0,"cpu_recovery_threshold":1.0,"memory_error_threshold":80.0,"memory_recovery_threshold":70.0}' \
         http://localhost:8081/api/v1/config/analyzer/tenant-1
    ```
2. 10秒程度（キャッシュTTL）待機し、Analyzer のログを確認する：
    ```bash
    docker compose logs analyzer
    ```
3. **期待結果:** 以下のフォーマットでアラートログが出力されること。
    ```text
    **************************************************
    [ALERT] 異常状態を検知しました！
    - テナント: tenant-1
    - ホスト: (ホストID)
    - CPU使用率: ...% (閾値: 2.0%)
    - メモリ使用率: ...% (閾値: 80.0%)
    - 検知時刻: ...
    **************************************************
    ```

### 2-2. 復帰 (RECOVERY) の発生テスト

閾値を元に戻し、アラート状態から通常状態へ安全に復帰することを確認する。

1. `tenant-1` のCPU閾値を元に戻す（例：エラー15.0%、復帰10.0%）：
    ```bash
    curl -X POST -H "Content-Type: application/json" \
         -d '{"cpu_error_threshold":15.0,"cpu_recovery_threshold":10.0,"memory_error_threshold":80.0,"memory_recovery_threshold":70.0}' \
         http://localhost:8081/api/v1/config/analyzer/tenant-1
    ```
2. 10秒程度待機し、Analyzer のログを確認する：
    ```bash
    docker compose logs analyzer
    ```
3. **期待結果:** 以下のフォーマットで復帰ログが出力されること。
    ```text
    **************************************************
    [RECOVERY] 通常状態に復帰しました。
    - テナント: tenant-1
    - ホスト: (ホストID)
    - CPU使用率: ...% (復帰基準: < 10.0%)
    - メモリ使用率: ...% (復帰基準: < 70.0%)
    - 復帰時刻: ...
    **************************************************
    ```

### 2-3. テナント間の分離性の確認

特定のテナントがアラート状態になっても、他のテナントに影響が及ばないことを検証する。

1. ターミナルでエージェントログを確認し、`agent-tenant-1` と `agent-tenant-2` が双方ともに正常にデータを送信できていることを確認。
2. `tenant-1` でアラート（2-1の手順）を発生させる。
3. Analyzer のログを確認する。
4. **期待結果:** 
    - `[ALERT]` 通知は `tenant-1` に対してのみ発行されていること。
    - デフォルト設定（CPU 80%）で動作している `tenant-2` については通常状態（Normal）のまま維持され、通知が混同されたり無駄なアラートが発生したりしていないこと。

---

## 3. Redis 上の状態管理の確認

Analyzer が前回の状態を Redis に正しく保存できているかを確認する。

1. Redis Master コンテナに入り、状態が保存されているハッシュキーを確認する：
    ```bash
    docker exec -it redis-master redis-cli hgetall state:hosts
    ```
2. **期待結果:**
    - フィールド名が `tenant_id:host_id`（例：`tenant-1:04a7866630a9`）の形式で登録されていること。
    - 値が `"Normal"` または `"Alert"` になっており、現在の状態と一致していること。

---

## 4. トラブルシューティング

- **Config Manager 接続エラー:** ログに `警告: Config Manager からの設定取得に失敗しました` 等が出ている場合、Consul に `configmanager` サービスが正常登録されているか確認する（`curl http://localhost:8500/v1/health/service/configmanager` でPassing状態か）。
- **アラートが消えない:** ヒステリシスロジックにより、復帰閾値（例: 10%）を下回るまで通常状態に戻りません。設定したリカバリ閾値よりもCPU使用率がしっかりと下がっているかを確認してください。
