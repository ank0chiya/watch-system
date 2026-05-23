# Config Manager 動作確認ガイド

本ドキュメントは、コンテナ化された設定管理サーバー（`configmanager`）が正常に起動し、設計通りにConsulへのサービス登録、MongoDBへの設定の永続化を行えているかを確認するための手順である。

---

## 1. コンテナの起動状態確認

Config Manager が正しくビルドされ、実行されているかを確認する。

### 1-1. コンテナステータスの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose ps configmanager
    ```
2. **期待結果:** `STATUS` が `Up` または `running` になっていること。

### 1-2. 起動ログの確認

1. ターミナルで以下を実行：
    ```bash
    docker compose logs configmanager
    ```
2. **期待結果:** 以下のメッセージが順に出力されていること。
    - `MongoDB への接続に成功しました`
    - `Consulにサービスを登録しました: configmanager-1`
    - `Config Managerをポート8081で起動しました...`

---

## 2. サービスディスカバリ（Consul）の確認

Config Manager が自身の情報をConsulに正しく通知できているかを確認する。

### 2-1. Consul Web UI での確認

1. ブラウザで `http://localhost:8500` にアクセスする。
2. **期待結果:** `Services` 一覧に `configmanager` が存在し、ヘルスチェック（Health Checks）が緑色（Passing）になっていること。

### 2-2. HTTP API による確認

1. ターミナルで以下を実行：
    ```bash
    curl http://localhost:8500/v1/catalog/service/configmanager
    ```
2. **期待結果:** Config ManagerのIPアドレスとポート（8081）を含むJSONデータが返却されること。

---

## 3. APIリクエストとデータフローの確認

外部からの設定登録および取得、MongoDBへの永続化が正常に機能しているかを確認する。

### 3-1. 初期（デフォルト）設定の取得確認

1. 設定を登録していないテスト用のテナントID（例：`test-tenant`）で GET リクエストを送信する：
    ```bash
    curl -s http://localhost:8081/api/v1/config/analyzer/test-tenant
    ```
2. **期待結果:** システム全体の共通デフォルト値である以下のJSONが返却されること。
    ```json
    {"cpu_error_threshold":80,"cpu_recovery_threshold":70,"memory_error_threshold":80,"memory_recovery_threshold":70}
    ```

### 3-2. テナント別閾値設定の登録 (POST)

1. `test-tenant` 用の個別の閾値を設定する（例：CPU閾値を低めに設定）：
    ```bash
    curl -X POST -H "Content-Type: application/json" \
         -d '{"cpu_error_threshold":15.0,"cpu_recovery_threshold":10.0,"memory_error_threshold":80.0,"memory_recovery_threshold":70.0}' \
         http://localhost:8081/api/v1/config/analyzer/test-tenant
    ```
2. **期待結果:** レスポンスとして `OK` が返却され、`configmanager` のコンテナログに `設定を更新しました [Tenant: test-tenant, ...]` と出力されること。

### 3-3. 登録データの確認 (GET)

1. 再度同じテナントIDで GET リクエストを送信する：
    ```bash
    curl -s http://localhost:8081/api/v1/config/analyzer/test-tenant
    ```
2. **期待結果:** 3-2で設定した個別の閾値JSONが返却されること。
    ```json
    {"cpu_error_threshold":15,"cpu_recovery_threshold":10,"memory_error_threshold":80,"memory_recovery_threshold":70}
    ```

### 3-4. MongoDB 上の永続化データの確認

MongoDB に直接入って設定が正しく登録・永続化されているかを確認するには、以下のいずれかの方法を実行します。

#### 方法A: ワンライナーで確認する（簡易確認）
1. ターミナルで以下を実行し、コマンドを直接実行します：
    ```bash
    docker exec -it mongodb mongosh watch_system --eval "db.configs.find({tenant_id: 'test-tenant'})"
    ```
2. **期待結果:** `configs` コレクション内の、`tenant_id: "test-tenant"`, `component: "analyzer"` および設定情報がドキュメントとして格納されているデータが出力されること。

#### 方法B: MongoDB シェル (mongosh) に入って対話モードで確認する
1. ターミナルで以下を実行し、MongoDB コンテナの対話型シェルを起動します：
    ```bash
    docker exec -it mongodb mongosh
    ```
2. **期待結果:** `test>` のように MongoDB シェルのプロンプトが表示されること。
3. 監視システム用のデータベースに切り替えます：
    ```javascript
    use watch_system
    ```
    **期待結果:** `switched to db watch_system` と表示されること。
4. `configs` コレクションに登録されている全テナントの設定データを確認します：
    ```javascript
    db.configs.find()
    ```
    **期待結果:** `tenant-1`, `tenant-2`, `test-tenant` などのドキュメントの一覧が出力されること。
5. 特定のテナントの設定を絞り込み、整形して表示します：
    ```javascript
    db.configs.find({ tenant_id: "test-tenant" }).pretty()
    ```
6. シェルを終了します：
    ```javascript
    exit
    ```

---

## 4. トラブルシューティング

動作が期待通りでない場合は、以下の点を確認する。

- **MongoDB接続エラー:** `configmanager` のログに `MongoDB 接続に失敗しました` 等が出ている場合、`docker-compose.yml` 内の `MONGO_URI` が `mongodb://mongodb:27017` になっているか確認する。
- **バリデーションエラー:** `POST` 送信時に `400 Bad Request` が返る場合、リカバリ閾値がエラー閾値より小さくなっているか（ヒステリシスの整合性）、値が0から100の範囲内かを確認する。
