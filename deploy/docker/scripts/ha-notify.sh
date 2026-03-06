#!/bin/bash
# keepalived notify 脚本 — 控制 docker compose 容器启停 + HA 同步 cron
COMPOSE_DIR=/opt/cloudland/deploy/docker
CRON_FILE=/etc/cron.d/cloudland-ha-sync
STATE=$1

case "$STATE" in
    MASTER)
        # VIP 漂到本机，启动所有服务
        cd $COMPOSE_DIR && docker compose start
        # 启用同步 cron（MASTER 向 BACKUP 推送）
        [ -f ${CRON_FILE}.disabled ] && mv ${CRON_FILE}.disabled $CRON_FILE
        ;;
    BACKUP|FAULT)
        # VIP 离开本机，停止所有服务
        cd $COMPOSE_DIR && docker compose stop
        # 禁用同步 cron（BACKUP 不推送）
        [ -f $CRON_FILE ] && mv $CRON_FILE ${CRON_FILE}.disabled
        ;;
esac
