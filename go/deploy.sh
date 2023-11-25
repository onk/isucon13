#!/bin/bash

# ビルドファイルのパス
BUILD_FILE="./isupipe"

# リモートでの配置先ディレクトリ
REMOTE_DIR="/home/isucon/webapp/go/"

# リモートのユーザー名
REMOTE_USER="isucon"
# SCPで転送するサーバーのIPアドレス一覧
SERVERS=("54.64.103.102" "54.64.167.170" "35.74.153.38");

# 現在の日付を取得
DATE=$(date +"%Y%m%d_%H%M%S")

NGINX_ACCESS_LOG_PATH="/var/log/nginx/access.log"
NGINX_ERROR_LOG_PATH="/var/log/nginx/error.log"
MYSQL_SLOW_LOG_PATH="/var/log/mysql/mysql-slow.log"

for server in "${SERVERS[@]}"
do
  echo $server
  # ログをリネームして、新しいログファイルを作成
  ssh -t "${REMOTE_USER}@${server}" <<EOF
# ログファイルをリネーム
sudo mv $NGINX_ACCESS_LOG_PATH ${NGINX_ACCESS_LOG_PATH}_$DATE
sudo mv $NGINX_ERROR_LOG_PATH ${NGINX_ERROR_LOG_PATH}_$DATE
sudo mv $MYSQL_SLOW_LOG_PATH ${MYSQL_SLOW_LOG_PATH}_$DATE

# 新しいログファイルを作成して、権限を変更
sudo touch $NGINX_ACCESS_LOG_PATH
sudo touch $NGINX_ERROR_LOG_PATH
sudo touch $MYSQL_SLOW_LOG_PATH
sudo chmod 666 $NGINX_ACCESS_LOG_PATH
sudo chmod 666 $NGINX_ERROR_LOG_PATH
sudo chmod 666 $MYSQL_SLOW_LOG_PATH
EOF

    ssh -t "${REMOTE_USER}@${server}" "sudo service isupipe-go stop"
    echo "サーバー ${server} にビルドファイルを転送しています..."
    scp "${BUILD_FILE}" "${REMOTE_USER}@${server}:${REMOTE_DIR}"

    # SCPの結果をチェック
    if [ $? -eq 0 ]; then
        echo "サーバー ${server} への転送が完了しました。サービスを再起動します..."
        # SSH経由でリモートサーバー上でサービスを再起動
        ssh -t "${REMOTE_USER}@${server}" "sudo service isupipe-go restart"

        if [ $? -eq 0 ]; then
            echo "サーバー ${server} でのisupipeサービスの再起動が成功しました。"
        else
            echo "サーバー ${server} でのisupipeサービスの再起動に失敗しました！" >&2
        fi
    else
        echo "サーバー ${server} への転送に失敗しました！" >&2
    fi
done

echo "すべてのサーバーへのビルドファイル転送とサービスの再起動処理が終了しました。"
