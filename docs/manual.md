# 当日マニュアルの要点

原文: [manual-original.md](manual-original.md)（https://gist.github.com/whywaita/17f3ab1a8c1b7585c571f4f8afe5f531 、10:30 取得）

## スケジュール

- 10:00 開始 / **17:00 リーダーボード凍結**（自チーム分は更新される）/ 18:00 終了 / 20:00 結果発表
- **最終スコア = 競技中に最後に回したベンチ**。最後が FAIL ならスコアなし → 最後のベンチは確実に通る構成で回す

## アプリ ISUReal

写真共有 SNS（BeReal 風）。用語: region（asia / europe / america）、posting window（地域ごとの1日分の投稿期間、`localDate`、`onTimeDeadline`）、friend（双方向）、友達の友達、certified public account（一方向フォロー）、RealMoji、Memories、SecretKey。

- 投稿期間の開始時に、その地域の利用者へ通知を作る。クライアントは `GET /api/notifications` を **SSE** で購読
- 締切を過ぎても投稿期間が終わるまでは投稿できる。`lateSeconds` = 締切からの経過秒（切り捨て）、時間内なら 0
- 初期ユーザー: `yuma_kobayashi_186` / SecretKey `0f6-BAnyBELD2MA-t-fekeDtQN4ynqD0mfgOF8JWwWs`（asia）
- 管理者: `admin` / `password1234`（ベンチマーカーが投稿期間の開始に使う。変更・削除禁止）

## サーバー構成

| サーバ | ssh | 固定リージョン | 初期の役割 | Type |
|---|---|---|---|---|
| asia1 | `isucon2026-asia1` | asia | nginx + Go | c5.large |
| europe1 | `isucon2026-europe1` | europe | nginx + Go | c7i.large |
| america1 | `isucon2026-america1` | america | nginx + Go | c7i.large |
| asia2 | `isucon2026-asia2` | asia | 共有 PostgreSQL 16 | c5.large（EBS 6000 IOPS） |
| router | ログイン禁止 | - | 国別の転送、遅延・帯域制限 | c6in.large |

- ルーターは `X-Country-Code` で振り分ける: JP/KR/CN/AU → asia1、GB/DE/FR → europe1、US/CA/MX → america1。省略時は JP 扱い。不正な値は 400
- **サーバ間の通信もルーター経由で、リージョンをまたぐと遅延と帯域制限がかかる**。asia1 と asia2 は同じリージョン扱い
- 初期状態では europe1 と america1 が、リージョンをまたいで asia2 の DB を見ている → **ここが大きなボトルネックの候補**
- クライアント IP は `X-Client-IP` ヘッダーを使う（ベンチマーカーはユーザーごとに違う IP を指定する）
- 全台を使う構成。リージョンの変更は不可

## スコア

`スコア = Σ（採点件数 × 配点 × リージョン倍率）`。倍率: **asia 1 / europe 1.5 / america 1.5**。エラー件数による直接の減点はない。FAIL なら 0。

配点の大きいもの:

| 採点対象 | 配点 |
|---|---:|
| 投稿後の友達フィードの検証通過（最後の投稿期間） | 20 |
| エクスポート | 20 |
| 投稿後の友達フィードの検証通過（通常の投稿期間） | 10 |
| RealMoji の送信 | 5 |
| 退会 | 5 |
| コメントの投稿 / プロフィール画像の変更・削除 / Memories 月単位 | 3 |
| 画像取得 / 過去の投稿の画像取得 / ユーザー検索 / 友達申請の送信・承認 / Memories 日付単位 | 2 |
| その他（フィード閲覧、投稿詳細、プロフィール、通知受信、フォロー等） | 1 |

- 投稿後の友達フィード検証は「ユーザー × 投稿期間」ごとに1回。**投稿が受理され、その投稿期間が終わる前に**友達フィードの検証に成功することが条件
- 投稿期間ごとの1ユーザーあたり上限: フィード 20、画像 4、投稿詳細 8、RealMoji・コメント一覧 合計 8、プロフィール 4。RealMoji 送信とコメント投稿は各 5 回まで
- 投稿直後の画像と投稿詳細の確認は、閲覧の上限とは別に加点される
- Memories やエクスポートは件数ではなく、一連の確認の成功回数で数える
- ベンチマーカーはリダイレクトを追わない。503 は再試行するが、エラー率や応答時間の計測には入る

## ベンチの流れ

1. `POST /api/initialize`（失敗したら即 FAIL）
2. 整合性チェック、初期データの検証、ユーザーの準備
3. 負荷テスト（終了時点で未完了のリクエストは切断。スコアには影響しない）
4. 最終チェック

- initialize の応答は `{"language":"go","agent":"...","harness":"...","model":"..."}`。キー名は `language`（`lang` ではない）。agent / harness / model は集計用で、得点には使われない。AI を使うなら申告を書き換える（例: agent `claude-code`、harness `claude-code`、model `claude-opus-5-5`）
- 初期化は、追加したキャッシュや複製先も含めて、**全リージョンで整合した状態**に戻す。最終チェックで決まる制限時間内に終わらせる
- DB 初期化のコマンド: `~/webapp/sql/init.sh`

## FAIL になる条件（クリティカルエラー）

- 初期データのユーザーのログインが 401 になる
- フィードに、存在しない投稿や閲覧権限のない投稿が返る
- 閲覧権限のない投稿を取得できる
- 閲覧権限のない投稿への RealMoji や、コメント権限のない投稿へのコメントが受理される
- Memories やエクスポートで取得した履歴・画像が、初期データの期待値と一致しない
- 投稿の削除成功から 5 秒を過ぎても、その画像を取得できる

整合性の要件:

- **書き込みが成功したら、その後の読み取りに反映する。リージョンをまたいでも同じ**（キャッシュや複製を入れるときの最大の制約）
- 画像削除の猶予 5 秒は、公開範囲・友達関係・ブロックの反映には適用されない（それらは即時に反映する）
- 画像 URL の署名と期限の形式・検証結果は変えない。署名が不正、期限切れ、画像が無効のときは 403

## 環境のルール（失格に直結）

- 使ってよい計算資源は、CFn テンプレートどおりに起動した EC2 だけ。インスタンスタイプの変更なども失格
- 変更してはいけないもの:
  - `isucon-register.service` 関連のファイルと登録情報
  - `isuadmin` ユーザー（ファイル、権限、ログイン情報）
  - envcheck 関連: `/usr/local/bin/envcheck`、`/usr/local/bin/isucon-contest-envcheck`、`/etc/systemd/system/envcheck.service`、`/etc/systemd/system/envcheck-baseline.service`、`/etc/isucon/envcheck.json`、`/var/lib/isucon-envcheck/`
  - `isucon` 以外のユーザーと公開鍵
- SG は新しいポートの解放だけしてよい。22 / 443 / 8080 には触らない
- **禁止: ルーターを迂回するサーバ間通信やベンチマーカーとの通信、リージョン・遅延・帯域制限の変更や回避、ルーターへのログイン**
- 外部仕様（URI、メソッド、ステータス、JSON/HTML の構造と意味、静的ファイル、認証・閲覧制限・公開範囲・削除・画像署名、初期データ、初期化、ブラウザからの利用）は維持する
- ベンチマーカーだけを見分けて違う結果を返すのは禁止
- 外部のコンテナレジストリ（ECR 等）はデプロイ用に使ってよい（非公開にする）
- 環境の確認: `sudo /usr/local/bin/envcheck`（成功時は「全ての追試環境チェックをパスしました」、終了コード 0）。**設定を変えたら実行する**
- 終了時は4台とも起動しておく。終了後はアナウンスがあるまで接続・操作しない
- ポータルのサーバ一覧に複数スタックが混在していたら失格

## 追試

1. 4台とルーターを再起動 → envcheck → 4台が同じ VPC にあるか確認
2. ベンチを回す。**FAIL か最終スコアの 75% 以下なら再実行、3回目もダメなら失格**
3. もう一度再起動 → ベンチで書き込んだデータが取れるか確認 → フロントエンドの表示を確認

## 言語の切り替え

`sudo systemctl disable --now isureal-go.service && sudo systemctl enable --now isureal-{go,nodejs,perl,php,python,ruby,rust}.service`（PHP は nginx 設定の追加が必要）
