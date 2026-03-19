#!/bin/sh
# BUG-FIX: 用户通过 volume 挂载 data 目录时，宿主机目录权限(通常 root:root)会覆盖
# 镜像构建阶段的 chown，导致 UID 65534 无法写入 SQLite 数据库文件。
# 以 root 启动 → 修复权限 → exec 降权到 nobody(65534) 运行应用。
chown 65534:65534 /app/data
exec setpriv --reuid=65534 --regid=65534 --init-groups "$@"
