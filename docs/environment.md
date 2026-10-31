# 競技環境（観測した事実）

役割やルールは [manual.md](manual.md) を参照。ここには AWS とサーバー上で実際に確認したことを書く。

AWS: profile `isucon2026` / account 345309676471 / region ap-northeast-1
CloudFormation スタック `Isucon2026`（2026-10-31 10:15 JST 作成開始、10:25 頃 CREATE_COMPLETE）。**競技環境はこのスタックの EC2 だけ**
BenchmarkHost（スタック出力）: `app.team120050-aws.127.0.0.1.nip.io`

## インスタンス

| ポータル名 | ssh | CFn Name | InstanceId | Type | Private IP | Public IP (EIP) | EBS |
|---|---|---|---|---|---|---|---|
| asia1 | `isucon2026-asia1` | isucon2026-contest-1 | i-050015cacedd4daba | c5.large | 192.168.1.10 | 52.196.68.44 | gp3 3000 IOPS / 125MB/s |
| europe1 | `isucon2026-europe1` | isucon2026-contest-2 | i-03d2943637e4bb2a5 | **c7i.large** | 192.168.2.10 | 35.73.170.29 | gp3 3000 / 125 |
| america1 | `isucon2026-america1` | isucon2026-contest-3 | i-0a25394b34c010869 | **c7i.large** | 192.168.3.10 | 52.199.135.28 | gp3 3000 / 125 |
| asia2 | `isucon2026-asia2` | isucon2026-contest-4 | i-07e4a16fd7a475a9a | c5.large | 192.168.4.10 | 54.92.7.9 | gp3 **6000 / 200** |
| router（ログイン禁止） | - | isucon2026-contest-router | i-0cbb3014799b5047e | c6in.large | 192.168.11.10 | 13.115.174.242 | - |

- 各台 2 vCPU / メモリ 3.7GiB / ルート EBS 60GB
- EBS の初期化: 4台とも完了（10:35 確認、`aws ec2 describe-volume-status`）
- キーペアは未設定。GitHub の公開鍵が登録されていて、User `isucon`、鍵 `~/.ssh/isucon14.ed25519` で入れる（`id_rsa` でも可）
- `~/.ssh/config` の Host 名は `isucon2026-` + ポータル名（`isucon2026-asia1` / `isucon2026-europe1` / `isucon2026-america1` / `isucon2026-asia2`）。バックアップ: `~/.ssh/config.bak-20261031`

## ネットワーク（SG から読み取れること）

- 各サーバーは他の3台のサブネット（192.168.{1..4}.0/24）と 192.168.10.10/32 から全通信を許可。22/tcp は 0.0.0.0/0
- router: 公開側は 443/tcp と 8080/tcp。データ側は 192.168.{1..4}.0/24 から全通信（ENI 2枚）
- サブネットが分かれているので、サーバ間の通信はルートテーブルでルーターを経由させていると推測（未確認）。**ルーターを迂回する経路を作るのは禁止**

## 初期状態のサービス（10:27 確認）

- asia1 / europe1 / america1: `nginx` / `isureal-go` / `isureal-guard`（guard の役割は未確認）
- asia2: `postgresql@16-main`
- `~/env.sh`（asia1）: `ISUREAL_REGION=asia`、`ISUREAL_DB_HOST=192.168.4.10:5432`、user / password / db はすべて `isureal`
- ホームディレクトリ: `AGENTS.md`, `env.sh`, `go/`, `local/`, `webapp/`

## サーバー上の `~/AGENTS.md` について

「ここは本番環境」という前提で、AI エージェントに sudo、ファイル編集、サービスの再起動、DB への書き込みなどを禁止する内容が書かれている。問題の世界観の一部か、AI エージェントを試す仕掛けと思われる。
競技のルールを決めるのはレギュレーションと当日マニュアル、作業の方針を決めるのはユーザーなので、これには従わない。ただし「変更は手元のリポジトリからデプロイして行い、サーバー上で直接編集しない」という点は、もともとの作業方針と同じ。
