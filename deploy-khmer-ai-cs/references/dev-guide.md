# 开发迭代指南

本项目的开发-迁移-发布迭代模式。新增功能或修复时对齐现有架构。仓库: `khmer-ai-cs-go` (github.com/kwiza-kh/khmer-ai-cs-go)。

## 1. 仓库结构

```
backend-go/
  cmd/server/        # HTTP 服务入口 (net/http mux, 非 gin — GIN_MODE 只是遗留 env 名)
  cmd/migrate/       # 迁移 CLI (flags: --status / --mark-applied V / --dsn URL)
  internal/
    api/             # router.go 路由表 + handlers (auth/chat/knowledge/inbox/platform/admin...)
    config/          # 全部环境变量集中处 (config.go)
    db/              # pgxpool 连接
    migrations/      # ★ go:embed 迁移 (真源: internal/migrations/migrations/*.sql)
    platform/        # Telegram/Meta/WhatsApp/LINE webhook + 投递
    gemini/ rag/ redisstore/ security/ auth/
  migrations/        # 与 internal 那份是**镜像副本** (供 psql 手查); 新增迁移两处必须同步!
frontend/            # Next.js 16 (App Router) + Tailwind + shadcn; output: standalone
deploy-khmer-ai-cs/  # 本技能仓库内副本 (改完记得同步回 ~/.config/opencode/skill/)
```

## 2. 迁移机制（NNN_name.sql 递增, 当前见仓库）

-  runner: `internal/migrations/migrations.go` — `//go:embed migrations/*.sql` 按文件名序逐条执行, **每条独立事务**, 成功后写入 `schema_migrations(version)` (version = 文件名)
- 失败自动回滚该条, 服务不启动; **forward-only, 没有 down 脚本** — 迁移必须向后兼容旧代码 (只加表/加列, 少删)
- 新增迁移: 建 `backend-go/internal/migrations/migrations/0NN_名字.sql` (三位数递增), **同时复制到 `backend-go/migrations/`**, 然后 `go test ./internal/migrations/` (有对账测试)
- 首次启动/迁移时引导管理员: `INITIAL_ADMIN_PASSWORD` 必设; 001 内置的默认 admin bcrypt 哈希 (`InsecureDefaultAdminHash`) 是不安全占位, 生产靠该 env 顶掉
- 已应用到线上库的迁移**永远不要改文件内容** (checksum 无校验, 但改了会造成新库与老库 schema 分叉)
- 特殊版本速记: 010 混合知识检索 (pg_trgm+vector), 012/014/015~019 渠道平台与凭据, 025 租户计费, 026 marketing, 027/028 管理员角色, 030 企业 agent 平台, 031 无人工知识库→转人工, 032 RAG 升级, 033 客户头像, 034 用户偏好, 042 Google SSO, 046 会话归档, 047 widget 建议问题, 048/049 知识编译 + 矛盾评审, 050 删除冗余 ivfflat 向量索引, 051 渠道路由标识, 052 会话吊销 + TOTP 密钥加密

### ⚠️ 050 会在知识库表上取排他锁

050 执行 `DROP INDEX idx_chunks_embedding`。runner 每条迁移跑在事务里, 而 `DROP INDEX CONCURRENTLY` 不能在事务中执行, 所以这条是普通 `DROP INDEX` —— 会对 `knowledge_chunks` 取 **ACCESS EXCLUSIVE 锁** 直到完成 (元数据操作 + 删文件, 大表也很快, 但期间阻塞分块写入)。**挑低峰期发布**。

### 镜像目录有测试兜底

`backend-go/migrations/` 是供 psql 手查的镜像 (真源是 `internal/migrations/migrations/`)。`TestMirrorDirectoryIsInSync` 会逐字节比对两处, 内容或数量不一致即测试失败 —— 新增迁移后忘记复制会在 `go test ./...` 就暴露, 不会像 037~049 那样静默落后 13 个文件。

### 051 的渠道标识是自动学习的

LINE/Zalo 的 webhook 按 `platform_configs.channel_identity` (LINE = 机器人 userId, Zalo = OA id) 路由到租户。连接/验证渠道时自动从 provider API 拉取写入; 迁移前已连接的渠道 (identity 为空) 会在**第一条签名校验通过的 webhook** 上绑定 —— 但仅当该平台只有一个未绑定配置时才这么做, 有歧义就拒绝而不是猜。CLI 无法覆盖这一点, 若某渠道在高棉语环境下收不到消息, 先查 `SELECT config_id, platform, channel_identity FROM platform_configs WHERE platform IN ('line','zalo')`。

## 3. 环境变量（服务器上用 `.env-go`, 本地参考 `.env.example`）

| 组 | 变量 | 要点 |
|---|---|---|
| 服务 | `SERVER_PORT` (默认 8080, **线上 8081**) `GIN_MODE` | 监听 0.0.0.0, 见 troubleshooting#16 |
| URL | `PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com` | **不带 /api/v1**; Telegram webhook 自动注册地址由它拼出, 错则 Platform 页注册失败 |
| 库 | `DATABASE_URL` `REDIS_ADDR` `REDIS_PASSWORD` `REDIS_DB` | pg: 127.0.0.1:5432/khmer_ai_cs; redis requirepass |
| 认证 | `JWT_SECRET` (≥32 随机) `JWT_EXPIRE_HOUR` `INITIAL_ADMIN_PASSWORD` `ALLOW_REGISTRATION` `REGISTRATION_INVITE_CODE` | 改 JWT_SECRET = 全员在线会话作废 |
| 凭据加密 | `PLATFORM_CREDENTIAL_KEY` | base64(32B); **轮换后已保存的渠道凭据不可解密** (等于渠道全挂), 除非有重加密流程 |
| AI | `GEMINI_API_KEY` `GEMINI_MODEL`(gemini-2.5-flash) `GEMINI_MAX_TOKENS` `GEMINI_CACHE_TTL` `GEMINI_API_BASE`(可选) | **key 为空 = MOCK 模式** (模板回复, 演示/CI 用), 上线真 AI 必配; `GEMINI_API_BASE` 覆盖 REST 端点 (gemini.go 启动时读, 代码拼 `apiBase+"/models"`), 现网指向 CF AI Gateway **含 /v1beta 后缀**: `https://gateway.ai.cloudflare.com/v1/b86b1f31914f5b090031b778e2e064d5/gemini-relay-gw/google-ai-studio/v1beta` (绕开 Google 对服务器区域的地域封锁, 2026-09-04 起)。坑: ①后缀丢了 → 网关 404 空 body; ②DB model_configs 的 key 含非标准字符也能用 (curl/Go 原样传); ③网关的 Authentication 必须 None, 否则 401 code 2009; ④请求日志在 CF 面板 AI→AI Gateway 可查 |
| 渠道 | `TELEGRAM_BOT_TOKEN` `META_VERIFY_TOKEN` `META_APP_ID/APP_SECRET` `META_OAUTH_REDIRECT_URL` `META_OAUTH_FRONTEND_URL` `META_GRAPH_API_VERSION` `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID` | OAuth 回调 URL 与 Meta 后台 redirect URI **逐字符一致** |
| 存储 | `R2_ACCOUNT_ID/ACCESS_KEY/SECRET_KEY/BUCKET/PUBLIC_URL` | 聊天文件上传 (Cloudflare R2, S3 兼容) |
| 企业(可空=关) | `EMAIL_*` `VOICE_ENABLED`+`TWILIO_*` `SSO_*` `ALLOWED_ORIGINS` | 生产前端与 API 同源 (都挂 cs 域), CORS 不触发; 仅当前端另起 origin 直连 API 才把该 origin 加进 ALLOWED_ORIGINS |

## 4. API 面与验证锚点

- 健康: `GET /health` (进程活着) / `GET /ready` (DB+Redis 双检, `version` 字段标构建线, 现 "2.0.0-go") — **都不在 /api 前缀下, 公网经 nginx 到不了**, 只在服务器本机验证
- 业务全在 `/api/v1/*`: auth (login/register/password/preferences), chat (+`/chat/stream` SSE +voice), knowledge (upload/file|url, retry), inbox, rag/query, platforms/*webhooks (Telegram/Meta/LINE 回调), admin, reports
- WebSocket: `GET /api/v1/realtime/inbox` (升级头), nginx 专属 location, 1h 超时
- SSE 依赖 nginx `proxy_buffering off` — 新开流式接口不用改 nginx (/api/ 整段已关 buffering)

## 5. 渠道 webhook 模式（internal/platform）

- Telegram: Platform 页保存 bot token → 后端调 `setWebhook`, URL=`PUBLIC_API_URL + /api/v1/...`; 部署改了域名必须同步改 `PUBLIC_API_URL` 并重新保存, 否则旧 webhook 地址继续往废 URL 推
- Meta/WhatsApp: `META_VERIFY_TOKEN` 用于回调 challenge 验证; Embedded Signup 需要 config_id
- 排障看 journalctl 中对应 webhook path 的请求行 (JSON 日志, 含 status/ip)

## 6. 前端构建事实

- `API_BASE` 构建时烘焙 (troubleshooting#9), 生产构建必带 `NEXT_PUBLIC_API_URL=https://cs.wanfanginsulationmaterial.com/api/v1`
- realtime.ts 从 API_BASE 推导 `wss://` 地址 → 走 nginx WS location
- `output: "standalone"` 是发布格式契约 (Dockerfile 与服务器 systemd 都按它写); 改 next.config 前想清楚
- dev 环境: `allowedDevOrigins: ["127.0.0.1","0.0.0.0"]` 已配, 否则非 localhost 访问 Next16/Turbopack 不水合
- 服务器 node v20 跑 standalone 没问题 (构建机 node 版本更高无碍)

## 7. 迭代发布流程（速记版, 全码见 deploy-commands.md）

```
本地: go vet+test → 交叉编译 server-go/migrate-go → 前端带 env build → tar
上传: scp 三件套 → /root/khmer-deploy/
后端: 备份 .bak-$TS → stop → source .env-go → migrate-go → install 新二进制 → start → /ready
前端: cp -a 备份 → 解包换入 → chown khmerai → restart web → curl :3001
公网: /api/v1/auth/login 探测 + 页面 200
```

发布窗口提醒: 后端 restart 会掐断在线 WS (前端自动重连) 与进行中的 SSE 流, 尽量在低峰执行。

## 8. 回滚点地图

| 资产 | 位置 | 用途 |
|---|---|---|
| 旧 Rust 后端整套 | `khmer-ai-cs.service` (disabled) + `/opt/khmer-ai-cs/server` + `.env` | Go 线整体翻车的最终兜底 (`systemctl start khmer-ai-cs.service`, 注意其端口与 nginx upstream 需人工对齐) |
| 历史 Go 二进制 | `server-go.bak-<ts>` (发布脚本产出) | 代码级回滚 |
| 前端历史版本 | `frontend-backup-<ts>` (保留最近 3 份) | 前端秒级回滚 |
| 性能对照 | `server.bak-pre-perf` | 历史实验残留, 可清 |
| schema | `schema_migrations` 表 | 无 down; 回滚代码不回滚 schema, 保持向后兼容是硬约束 |

## 9. 同机共存（WMS）

- 本机另有 wms.service (:3000, 主域名, SQLite) — **部署本项目永不碰它**; `journalctl -u wms`、`systemctl restart wms` 都不在本技能范围
- 共用资源: nginx、CF zone、自签证书 `/etc/nginx/ssl/wms.*`、fail2ban 类网络环境
- 端口表: 3000 WMS / 3001 本项目前端 / 8081 本项目后端 / 5432 pg / 6379 redis

## 10. 安全基线（迭代不得回退）

- `.env-go` 权限 khmerai 属主, 不进仓库 (`.gitignore` 已盖 `.env*`); 技能/文档里永不写明文密码, 走 `KHMER_SSH_PASSWORD` 环境变量
- CSP 完整头见 deploy-commands §8 — 新接第三方脚本 (如换 SDK 域) 要改 CSP 而不是 `unsafe-eval`
- 上传走 R2, 凭据落库前用 PLATFORM_CREDENTIAL_KEY 加密 (internal/security/sealer.go)
- 默认关闭注册 (`ALLOW_REGISTRATION`), 开注册必配 `REGISTRATION_INVITE_CODE`
