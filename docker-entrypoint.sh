#!/bin/sh
# 引导逻辑（配合 /app/bin/.trae_source 来源标记）：
#   - 卷内无二进制，或上次播种来源是镜像（image）→ 用镜像内副本播种/覆盖
#     （镜像升级重建后，旧播种自动被新镜像版本替换）
#   - 来源是热更新（hotupdate）→ 保留卷内二进制，不覆盖
#   - 卷不可写（属主非容器用户）→ 回退镜像内二进制，服务可用但热更新无效
MARK_FILE=/app/bin/.trae_source
MARK=$(cat "$MARK_FILE" 2>/dev/null || echo "")
if [ ! -x /app/bin/trae-relay ] || [ "$MARK" = "image" ]; then
    if cp /usr/local/bin/trae-relay /app/bin/trae-relay 2>/dev/null; then
        chmod +x /app/bin/trae-relay 2>/dev/null
        echo image > "$MARK_FILE" 2>/dev/null
    fi
fi
if [ -x /app/bin/trae-relay ]; then
    exec /app/bin/trae-relay
fi
echo "[entrypoint] bin 卷不可写，回退到镜像内置二进制（热更新不可用，chown 10001 卷目录可修复）"
exec /usr/local/bin/trae-relay
