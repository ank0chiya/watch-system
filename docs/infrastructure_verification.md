# インフラ基盤 動作確認ガイド (Consul / Redis / MongoDB)

本ドキュメントは、Docker Composeで構築した監視システムのインフラ基盤が正常に稼働し、各サービスの連携および高可用性（HA）機能が正しく動作することを確認するための手順である。

## 1. Consul の動作確認
Consulはシステムのサービスレジストリとして機能する。

### 1-1. Web UI によるステータス確認
1. ブラウザで `http://localhost:8500` にアクセスする。
2. **期待結果:** Consulのダッシュボードが表示され、`Services` タブに `consul` が正常（緑色）に表示されていること。

### 1-2. CLI によるクラスター状態の確認
1. ターミナルで以下を実行：
   ```bash
   docker exec -it consul-server consul members
   ```
2. **期待結果:** `Status` が `alive`、`Type` が `server` のノードが表示されること。

---

## 2. Redis Sentinel (高可用性) の動作確認
Redisのマスター/レプリカ構成および、Sentinelによる自動フェイルオーバーを確認する。

### 2-1. 現在のマスター特定
1. ターミナルで以下を実行：
   ```bash
   docker exec -it redis-sentinel redis-cli -p 26379 sentinel get-master-addr-by-name mymaster
   ```
2. **期待結果:** 現在のマスター（初期状態では `redis-master` のIP）が表示されること。

### 2-2. フェイルオーバー試験 (疑似障害)
1. Sentinelのログをリアルタイム表示する：
   ```bash
   docker compose logs -f redis-sentinel
   ```
2. 別のターミナルで、現在のマスターを一時停止（疑似フリーズ）させる：
   ```bash
   docker pause redis-master
   ```
3. **期待結果:** - ログに `+sdown` (主観的ダウン)、`+odown` (客観的ダウン) が出力されること。
   - `+switch-master` が出力され、`redis-replica` のIPアドレスへ切り替わること。

### 2-3. 自動復旧試験
1. 一時停止した旧マスターを再開させる：
   ```bash
   docker unpause redis-master
   ```
2. **期待結果:** ログに `+convert-to-slave` が出力され、旧マスターが新しいマスターの「レプリカ」として自動的に再組み込みされること。

---

## 3. MongoDB の動作確認
データの最終保存先として、読み書きが可能であることを確認する。

### 3-1. 接続およびデータ操作テスト
1. MongoDBシェルを起動：
   ```bash
   docker exec -it mongodb mongosh
   ```
2. シェル内で以下のコマンドを順に実行：
   ```javascript
   // データベースの作成/切り替え
   use watch_system

   // テストデータの挿入
   db.metrics.insertOne({ host_id: "test-server", cpu: 12.5, memory: 64.0 })

   // データの検索
   db.metrics.find()
   ```
3. **期待結果:** - `insertOne` で `acknowledged: true` が返ること。
   - `find` で挿入したデータが正しく表示されること。

---

## 4. クリーンアップと再構築 (必要に応じて)
検証によって設定ファイルが書き換わった状態をリセットしたい場合は、以下のコマンドを使用する。
```bash
docker compose down
docker compose up -d
```