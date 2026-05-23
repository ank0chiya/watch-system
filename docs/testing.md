# 分散型監視システム テスト仕様・実行手順書

本ドキュメントは、分散型監視システムにおける各テスト（単体テスト、E2E テスト、手動結合テスト）のテストケース定義およびその実行手順についてまとめたものである。

---

## 1. テスト構成概要

本システムのテストスイートは、以下の3つのレイヤーで構成されている。

1. **単体テスト (Unit Test)**:
   外部依存関係（DBやRedis、ネットワークなど）を排除、またはモック/ローカル検証として分離し、各コンポーネントの「純粋なロジック」を保証するテスト。
2. **E2E テスト (End-to-End Test)**:
   実際に起動している Consul, MongoDB, Redis, API サーバー, Config Manager に実ネットワークを介してリクエストを送り、設定の永続化から状態遷移・検知までの一連のフローを自動検証するテスト。
3. **手動結合テスト (Manual Integration Test)**:
   テスト用の Alpine ダミーコンテナを起動・停止させることで、実際の ICMP（Ping）の瞬断からアラート発報、復帰までの一連のプロセスを目視検証するテスト。

---

## 2. 単体テスト (Unit Test) 仕様

単体テストは、Go の標準テストライブラリを利用し、各サービスディレクトリ配下で実行可能である。

### 2-1. Config Manager バリデーションテスト
* **ソースファイル:** `cmd/configmanager/validation_test.go`
* **テスト対象:** `validateConfig(config AnalyzerConfig) error`
* **テストケース:**
  * **`TestValidateConfig_Valid`**: 正常な設定値（CPU/メモリ閾値 80/70%、Ping数 5、ロスト閾値 3 等）がバリデーションをエラーなく通過することを確認。
  * **`TestValidateConfig_InvalidThresholds`**: 以下の異常な入力に対して、期待通りのバリデーションエラーを返すことを検証。
    * **`CPU_Error_Threshold_Too_High`**: エラー閾値が 100% を超過している場合。
    * **`CPU_Recovery_Equal_To_Error`**: CPUの復帰閾値がエラー閾値以上（＝同等）である場合。
    * **`Memory_Recovery_Greater_Than_Error`**: メモリの復帰閾値がエラー閾値より大きい場合。
    * **`PingCount_Too_Small`**: Ping 送信数が `0`（1〜20の範囲外）である場合。
    * **`LostThreshold_Greater_Than_PingCount`**: ロスト判定閾値（6）が Ping 送信数（5）を上回っている場合。
    * **`PingInterval_Too_Small`**: パケット送信間隔が `50ms`（100ms〜5000msの範囲外）である場合。
    * **`CollectInterval_Too_Small`**: データ収集間隔が `1s`（2s〜300sの範囲外）である場合。

### 2-2. Agent Ping 疎通テスト
* **ソースファイル:** `cmd/agent/ping_test.go`
* **テスト対象:** `pingHost(target string, timeoutMS int) bool`, `collectPingMetrics(...) []PingResult`
* **テストケース:**
  * **`TestPingHost_Localhost`**: ローカルホスト (`127.0.0.1`) への Ping が正常に通り、`sent == 1`, `received == 1` となることを確認。
  * **`TestPingHost_InvalidHost`**: 存在しないドメイン（`invalid.local` 等）への Ping が失敗し、`sent == 1`, `received == 0` となることを確認。
  * **`TestCollectPingMetrics`**: 複数ターゲットかつ複数回 (例: 2回) の Ping スキャンが指定した通りに実行され、送信数と受信数のカウントが一致（または期待通りに失敗）することを確認。

### 2-3. Analyzer 状態判定テスト
* **ソースファイル:** `cmd/analyzer/analysis_test.go`
* **テスト対象:** `evaluateState(payload MetricPayload, conf AnalyzerConfig, lastState string) string`
* **テストケース:**
  * **`All_Normal_Starts_Normal`**: 初期状態が `Normal` で、リソース・Pingともに正常なとき、状態が `Normal` を維持することを確認。
  * **`CPU_Alert`**: CPU 使用率がエラー閾値を超過したとき、状態が `Alert` に遷移することを確認。
  * **`Ping_Alert_Lost_3_Of_5`**: ロストパケット数（3）がロスト判定閾値（3）に達したとき、状態が `Alert` に遷移することを確認。
  * **`Ping_Normal_Lost_2_Of_5`**: ロストパケット数（2）が閾値（3）未満のとき、状態が `Normal` を維持することを確認。
  * **`Hysteresis_Keep_Normal`**: 状態が `Normal` の際、リソース使用率が復帰閾値を超えてエラー閾値未満の「中間グレーゾーン」にあるとき、状態が `Normal` に維持されること（ヒステリシス動作）を確認。
  * **`Hysteresis_Keep_Alert`**: 状態が `Alert` の際、リソース使用率が復帰閾値以上でエラー閾値を下回ったとき、`Alert` が維持されることを確認。
  * **`Recovery_To_Normal`**: 状態が `Alert` の際、リソース使用率が復帰閾値を下回り、かつ Ping ロストも解消された場合に、正常に `Normal` へ復帰することを確認。
  * **`No_Recovery_If_Ping_Still_Down`**: リソースが復帰基準に達していても、Ping のロスト数がしきい値以上の場合は `Alert` 状態が維持されることを確認。

---

## 3. E2E テスト (End-to-End Test) 仕様

E2E テストは、Docker Compose でローカル環境に起動している実コンテナ群（MongoDB、Redis、API サーバー、Config Manager）とネットワーク通信を行って検証する。

* **ソースファイル:** `tests/e2e/e2e_test.go`
* **前提条件:** `docker compose` で全コンテナが起動していること。
* **テストシナリオ:**
  1. **`TestE2EWatchSystem` (メイン監視シナリオ)**
     - **初期クリーンアップ**: MongoDB から `e2e-tenant` の古い設定を、Redis から `state:hosts` の該当ステータスを削除。
     - **ステップ1: デフォルト設定取得の検証**: `/api/v1/config/analyzer/e2e-tenant` に GET し、未登録のテナントに対して共通のデフォルト値が取得できることを確認。
     - **ステップ2: 閾値設定の登録とMongoDB反映**: CPU閾値（エラー15%、復帰10%）などの設定データを POST 送信し、MongoDB に正しく Upsert されることを確認。
     - **ステップ3: メトリクス送信と Redis Streams 登録**: 閾値を超えるメトリクス（CPU 20%）を含む JSON ペイロードを `apiserver` に POST。Redis Stream (`metrics_stream`) に即座に格納されることを確認。
     - **ステップ4: アラート遷移判定**: Analyzer が Stream から Pull して判定処理を行い、Redis ハッシュ `state:hosts` 上の状態が `"Alert"` に更新されることをポーリング監視で確認。
     - **ステップ5: 正常復帰判定**: 復帰基準以下のメトリクス（CPU 5%）を送信し、Redis ハッシュ上の状態が `"Normal"` に遷移することを確認。
  2. **`TestE2EConfigValidation` (バリデーション API 検証)**
     - Config Manager に対して意図的に不正な設定（復帰閾値 ＞ エラー閾値）を POST し、API が `400 Bad Request` を返すことを検証。

---

## 4. 手動結合テスト (Manual Integration Test) 仕様

コンテナ環境を利用した手動による ICMP 障害検知および復帰のテストには、以下の2通りのアプローチ（パターン）が存在する。

---

### パターンA: コンテナ停止による物理障害検知

コンテナそのものを停止させて、疎通不能にするテスト手順。

#### A-1. Ping ロストによるアラート (ALERT) 検知テスト
1. **検証対象ホストのコンテナを停止**:
   ```bash
   docker compose -f deployments/docker-compose.yml stop target-host-1
   ```
2. **ログの確認**:
   約10〜15秒後（エージェントの 5秒収集周期 + Ping 5回送信）に、Analyzer のログを確認。
   ```bash
   docker compose -f deployments/docker-compose.yml logs analyzer
   ```
   * **期待結果:** `[ALERT] 異常状態を検知しました！` というログブロックが出力され、`Ping Target: target-host-1, Sent: 5, Received: 0 (Lost: 5, Threshold: 3)` と表示されていること。

#### A-2. Ping 疎通復帰による通常状態 (RECOVERY) 検知テスト
1. **コンテナの再開**:
   ```bash
   docker compose -f deployments/docker-compose.yml start target-host-1
   ```
2. **ログの確認**:
   数秒後に Analyzer のログを確認。
   * **期待結果:** `[RECOVERY] 通常状態に復帰しました。` というログブロックが出力され、`Ping Target: target-host-1, Sent: 5, Received: 5 (Lost: 0, Threshold: 3)` と表示されていること。

---

### パターンB: iptables による ICMP パケット遮断（ネットワーク障害検知）

コンテナは起動したまま、ターゲットコンテナの Linux カーネルフィルタ (`iptables`) を操作してパケットを破棄する、より本番環境に近いテスト手順。
※本手順を実行するため、`target-host-1`/`target-host-2` には `cap_add: [NET_ADMIN]` を有効化しています。

#### B-1. フィルタリング設定
`target-host-1`/`target-host-2` は `deployments/Dockerfile.target` にてビルドされ、すでに `iptables` がプレインストールされています。

1. **ICMP Echo Request (Ping) を遮断するルールを追記**:
   ```bash
   docker exec target-host-1 iptables -A INPUT -p icmp --icmp-type echo-request -j DROP
   ```

#### B-2. アラート (ALERT) 検知確認
1. **ログの確認**:
   約10〜15秒後に Analyzer のログを確認。
   ```bash
   docker compose -f deployments/docker-compose.yml logs analyzer
   ```
   * **期待結果:** `[ALERT] 異常状態を検知しました！` というログブロックが出力され、`Ping Target: target-host-1, Sent: 5, Received: 0 (Lost: 5, Threshold: 3)` と表示されていること。（コンテナ自体は `running` のまま障害を検知）

#### B-3. パケット遮断解除と通常状態 (RECOVERY) 検知確認
1. **iptables ルールをフラッシュ（全削除）して通信を復帰**:
   ```bash
   docker exec target-host-1 iptables -F
   ```
2. **ログの確認**:
   数秒後に Analyzer のログを確認。
   * **期待結果:** `[RECOVERY] 通常状態に復帰しました。` というログブロックが出力され、`Ping Target: target-host-1, Sent: 5, Received: 5 (Lost: 0, Threshold: 3)` と表示されていること。

---

## 5. テスト実行コマンド一覧

コピー＆ペーストで簡単に実行するためのコマンド集。

### 5-1. すべてのテストの実行 (単体テスト & E2E テスト)
※実行の前に docker compose が起動している必要があります。
```bash
# プロジェクトルートにて実行
go test -v ./...
```

### 5-2. 単体テストのみを個別に実行
```bash
# Agent のみ
cd cmd/agent && go test -v

# Config Manager のみ
cd cmd/configmanager && go test -v

# Analyzer のみ
cd cmd/analyzer && go test -v
```

### 5-3. E2E テストのみを実行
```bash
# E2E のみ
cd tests/e2e && go test -v
```
