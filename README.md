# trae-relay

精简版 Trae CN 模型反代：把 Trae remote 会话通道包装成 OpenAI 兼容 API。
**Go 纯标准库实现，零第三方依赖**，单二进制，内存占用 ~10MB，镜像 ~15MB。

## 功能

- **API 出口**：`GET /v1/models`、`POST /v1/chat/completions`（流式 SSE + 非流式），`POST /v1/chat`、`POST /chat/completions` 兼容别名；SSE 心跳注释帧防网关断连
- **账号池**：JSON 批量导入、账号密码登录导入（实验性）、启用/禁用、顺序轮询 / 积分优先调度、每账号并发槽位与排队、连续错误自动冷却
- **签到**：每日定时自动签到（北京时间，错过自动补签）、单账号/一键全部签到、9074 风控自动轮换设备 ID + 指数退避重试
- **积分系统**：上游权益包汇总查询（支持无限额度），请求后异步刷新快照，作为积分优先调度数据源
- **消费记录**：按请求记录 tokens/积分/状态/耗时（滚动 500 条）+ 每日聚合统计（北京时间，持久化）
- **Web 控制台**：`/admin` 单页（概览/账号/记录/设置/更新五个页签），控制台修改即时生效
- **热更新**：从 GitHub Releases 拉取预编译二进制 → 校验解压 → 原子替换 → 进程原位重启；支持自动定期检查
- **Docker 预构建镜像**：CI 双架构（amd64/arm64）推送 GHCR，`docker compose up -d` 即用

## 快速开始（Docker，推荐）

```bash
mkdir -p trae-relay && cd trae-relay
curl -O https://raw.githubusercontent.com/wangct233-source/trae-relay/main/docker-compose.yml
docker compose up -d
```

预构建镜像（发布 Release 后自动构建）：

```bash
docker pull ghcr.io/wangct233-source/trae-relay:latest
```

打开 `http://服务器:8080/admin` 导入账号即可。公网部署**必须**在 compose 中设置
`API_KEYS` 与 `ADMIN_PASSWORD`，或改绑 `127.0.0.1` 后用 nginx 反代。

## 本地运行

```bash
go build -ldflags "-X main.version=vX.Y.Z" -o trae-relay .
ADMIN_PASSWORD=xxx ./trae-relay   # 默认监听 :8080，数据写 ./data
```

## 账号导入

控制台「账号池 → JSON 批量导入」粘贴：

```json
[
  {"token": "Cloud-IDE-JWT 的 token 部分", "refresh_token": "可选", "user_id": "可选", "label": "主号"},
  {"refresh_token": "仅 refreshToken 也可，会自动换 JWT"}
]
```

- `token`：Trae CN 客户端/网页抓包的 JWT（`Authorization: Cloud-IDE-JWT <token>`）
- 同 `user_id`（或显式 `id`）再次导入为更新，不会重复
- **密码登录导入**为实验性：上游端点未公开文档化，路径可用 `TRAE_LOGIN_PATH` 覆盖；失败时建议用 refresh_token 方式

## API 调用

```bash
curl http://服务器:8080/v1/chat/completions \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{"model":"auto","stream":true,"messages":[{"role":"user","content":"你好"}]}'
```

`model` 取 `/v1/models` 列表；`auto` 走上游自动绑定，显式模型名默认同样走 auto 策略，
需要精确绑定时设置 `TRAE_MODEL_STRATEGY=manual` + `TRAE_CUSTOM_MODEL_JSON`。

## 热更新

1. 发布流程：推送 `vX.Y.Z` tag → GitHub Actions 自动构建双架构二进制上传 Release，并推送 GHCR 镜像
2. 更新方式（二选一）：
   - 控制台「更新」页：检查更新 → 下载并升级（自动重启）
   - 开启 `AUTO_UPDATE` 定期自动升级
3. Docker 部署时 compose 已把 `/app/bin` 挂载为卷：热更新替换的二进制在容器重建后依然保留

前提：环境变量 `UPDATE_REPO=<owner>/<repo>` 指向你的 GitHub 仓库。

## 配置

全部环境变量见 [.env.example](.env.example)。控制台可运行时修改：
调度模式、自动签到开关与时间、自动更新开关。

## 结构

```
main.go                  装配：路由、鉴权、后台任务、优雅退出
internal/config/         环境变量集中解析
internal/store/          JSON 持久化（原子写 + 锁）
internal/account/        账号模型 / Trae 管理 API / 账号池调度 / 密码登录
internal/relay/          remote chat_sessions 协议 + OpenAI 兼容出口
internal/usage/          消费记录与每日统计
internal/checkin/        签到调度与 9074 退避
internal/admin/          管理 API + 内嵌控制台单页
internal/updater/        GitHub Releases 热更新
```

## 与 trae2api-cn 的取舍

| | 本项目 | Trae2api-cn |
|---|---|---|
| 依赖 | 零（Go 标准库） | Python + FastAPI/httpx |
| 内存/镜像 | ~10MB / ~15MB | ~100MB+ / ~150MB |
| 上游通道 | 1 条（remote，最稳） | 6 条可切换 |
| 定位 | 低占用、易部署、易维护 | 功能全面 |

## 免责

仅供学习研究。上游为非公开协议，随 Trae 更新可能失效；使用产生的一切后果由使用者自行承担，请遵守 Trae 服务条款。
