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
| URL | `PUBLIC_API_URL=https://<部署域名>` | **不带 /api/v1**; Telegram webhook 自动注册地址由它拼出, 错则 Platform 页注册失败 |
| 库 | `DATABASE_URL` `REDIS_ADDR` `REDIS_PASSWORD` `REDIS_DB` | pg: 127.0.0.1:5432/khmer_ai_cs; redis requirepass |
| 认证 | `JWT_SECRET` (≥32 随机) `JWT_EXPIRE_HOUR` `INITIAL_ADMIN_PASSWORD` `ALLOW_REGISTRATION` `REGISTRATION_INVITE_CODE` | 改 JWT_SECRET = 全员在线会话作废 |
| 凭据加密 | `PLATFORM_CREDENTIAL_KEY` | base64(32B); **轮换后已保存的渠道凭据不可解密** (等于渠道全挂), 除非有重加密流程 |
| AI | `GEMINI_API_KEY` `GEMINI_MODEL`(gemini-2.5-flash) `GEMINI_MAX_TOKENS` `GEMINI_CACHE_TTL` `GEMINI_API_BASE`(可选) | **key 为空 = MOCK 模式** (模板回复, 演示/CI 用), 上线真 AI 必配; `GEMINI_API_BASE` 覆盖 REST 端点 (gemini.go 启动时读, 代码拼 `apiBase+"/models"`), 现网指向 CF AI Gateway **含 /v1beta 后缀**: `https://gateway.ai.cloudflare.com/v1/<CLOUDFLARE_ACCOUNT_ID>/gemini-relay-gw/google-ai-studio/v1beta` (绕开 Google 对服务器区域的地域封锁, 2026-09-04 起)。坑: ①后缀丢了 → 网关 404 空 body; ②DB model_configs 的 key 含非标准字符也能用 (curl/Go 原样传); ③网关的 Authentication 必须 None, 否则 401 code 2009; ④请求日志在 CF 面板 AI→AI Gateway 可查; ⑤**生产实际模型由 DB `model_configs.is_default` 覆盖** (启动时 HotReload 日志 `Gemini configured from database model config`), 上面的 `GEMINI_MODEL` 只在没有 DB 配置时生效 —— 排查"模型不对"先看那行启动日志, 别只看 `.env-go` |
| Jev | `TYPESAFE_API_KEY` (缺失 = 客户端为 nil, 所有接入点走旧路径) `JEV_KEEPWARM_SEC`(默认 45, 0=关) `JEV_GUARD_BUDGET_MS`(默认 3000) `JEV_ROUTE_BUDGET_MS`(默认 4000) `JEV_RULE_SOLO_MIN`(默认 0.90) `JEV_TURN_ESCALATE_MIN`(默认 0.60) `JEV_RULE_CONFIRM_MIN`(默认 0.70) | 类型化判断模型 (单端点 `api.typesafe.ai/v1/systemone`, 无 Go SDK), 决策点优先走它。**保活是前提**: 生产主机实测冷连接 0.64-4.24s / 热连接 0.22-0.42s (纯 TLS 握手单独就 0.44-3.62s), 而本项目流量稀疏 ⇒ 几乎每次都是冷启动 ⇒ 预算被打穿后决策回落给**更慢**的快模型 (实测 Jev turn 354ms vs 快模型 3234ms, 快 9.1 倍) —— 超时等于把准确判断换成乱升级。探针间隔必须 < `http.Transport` 的 `IdleConnTimeout`(90s), 否则连接在两次探针之间就已经死了。⚠️ `JEV_RULE_SOLO_MIN` 与 `JEV_TURN_ESCALATE_MIN` **是两回事**: 前者是 TurnTriggerFor 的真正转人工安全阀, 后者只决定 judgeTurnJev 写进 verdict 的 Escalate 标记 (widget 等外部调用方读它)。两者曾经共用一个变量, 各有各的默认值 —— 调一个会静默带动另一个, 已拆开 |
| 渠道 | `TELEGRAM_BOT_TOKEN` `META_VERIFY_TOKEN` `META_APP_ID/APP_SECRET` `META_OAUTH_REDIRECT_URL` `META_OAUTH_FRONTEND_URL` `META_GRAPH_API_VERSION` `META_WHATSAPP_EMBEDDED_SIGNUP_CONFIG_ID` | OAuth 回调 URL 与 Meta 后台 redirect URI **逐字符一致** |
| 存储 | `R2_ACCOUNT_ID/ACCESS_KEY/SECRET_KEY/BUCKET/PUBLIC_URL` | 聊天文件上传 (Cloudflare R2, S3 兼容) |
| 企业(可空=关) | `EMAIL_*` `VOICE_ENABLED`+`TWILIO_*` `SSO_ENABLED` `SSO_PROVIDER` `SSO_OIDC_ISSUER` `SSO_OIDC_CLIENT_ID/SECRET` `SSO_REDIRECT_URL` `SSO_FRONTEND_URL` `SSO_ALLOW_SIGNUP` `ALLOWED_ORIGINS` | 生产前端与 API 同源 (都挂 cs 域), CORS 不触发; 仅当前端另起 origin 直连 API 才把该 origin 加进 ALLOWED_ORIGINS |
| ⚠️ SSO 验签 | `SSO_SKIP_ID_TOKEN_VERIFY` (**默认 false**) | Google 登录回调的 id_token **RS256 签名校验开关** (`internal/config/config.go` 读, 生效点 `api/google_sso.go` 的 `verifyGoogleIDToken`)。设 true = **整段跳过 JWKS 验签**, 只信服务器到 Google 的 TLS 通道 —— 该函数的注释写明后果是 **account impersonation**: 能在这条出口链路上插手的人 (代理/中继/被劫持的出口/恶意 CA) 可以为**任意** Google 账号签发登录, 包括本库已存在的账号。**仅当该主机确实访问不到 `https://www.googleapis.com/oauth2/v3/certs` 时才开**; 开了就等于把 Google 登录的账号真实性全部交给传输层。JWKS 拉取失败先修出口/代理/DNS, **不要**改这个值当权宜之计。默认 false 是 fail-closed, 保持它 |

## 4. API 面与验证锚点

- 健康: `GET /health` (进程活着) / `GET /ready` (DB+Redis 双检) — **都不在 /api 前缀下, 公网经 nginx 到不了**, 只在服务器本机验证
  - `service` 恒为 `"relaychat"` (auth_handlers.go 写死), 不是项目名
  - `version` 是**构建期注入的 git 短哈希** (`-ldflags "-X khmer-ai-cs-go/internal/api.Version=$VERSION"`), 发布后必须核对它确认线上构建
- 业务全在 `/api/v1/*`: auth (login/register/password/preferences), chat (+`/chat/stream` SSE +voice), knowledge (upload/file|url, retry), inbox, rag/query, platforms/*webhooks (Telegram/Meta/LINE 回调), admin, reports
- **隐私合规**: `POST /api/v1/webhook/meta/data-deletion` — Meta 数据删除回调 (Platform Terms §3(d)(i)), 无鉴权、以 `signed_request` 的 HMAC-SHA256 验签; 需在 Meta App Dashboard → User data deletion 选 **Data deletion callback URL** 并填该地址 (选 "instructions URL" 则此端点永不触发)。审计写入 `deletion_requests`; 状态页 `GET /privacy/deletion-status?code=…` + 公开查询 `GET /api/v1/privacy/deletion-status?code=…`
- WebSocket: `GET /api/v1/realtime/inbox` (升级头), nginx 专属 location, 1h 超时
- SSE 依赖 nginx `proxy_buffering off` — 新开流式接口不用改 nginx (/api/ 整段已关 buffering)

## 4b. 运维任务

**每日维护 `khmer-maintenance.timer`** (03:00 金边 = 20:00 UTC; 主机跑 UTC, 所以 unit 里写的是 UTC —— **别按 UTC 的 03:00 改, 那是当地上午 10 点营业高峰**):

```
/opt/khmer-ai-cs/maintenance.sh   ← ExecStart
  ├─ ./retention            (dry-run, 把"将要删什么"写进 journal 留痕)
  ├─ ./retention --apply    (真正删)
  ├─ 裁剪 server-go.bak-*   只留最近 10 份
  └─ 裁剪 frontend-backup-* 只留最近 3 份
```

手动跑: `systemctl start khmer-maintenance.service`；看日志: `journalctl -u khmer-maintenance`。
停掉: `systemctl disable --now khmer-maintenance.timer`。

**`cmd/retention`** — 保留期。**默认 dry-run, 不传 `--apply` 什么都不删**, 且 dry-run 用完全相同的谓词与 LIMIT 计数, 所以它报的数字就是 `--apply` 会删的量。

| 规则 | 默认窗口 | 说明 |
|---|---|---|
| `platform_oauth_sessions` | 过期即删 | 浏览器 OAuth 往返的临时落点。**它的 `payload_json` 里存着明文 page_access_token**(没有 `platform_configs` 那份的加密), 所以放过期行 = 留明文凭据 |
| `rag_query_logs` | 90 天 | 客户问题原文的**第二份副本** |
| `notifications` | 90 天 | |
| `platform_inbound_events` | 已完成且 30 天 | `content` 列是原始客户消息 |
| `audit_logs` | **不删** | 需显式 `--audit-days N`; 审计留痕有正当长期用途, 且含 ip_address |
| `platform_outbox` | **不删** | 需显式 `--outbox-days N` |

每条规则独立事务 (一张表失败不影响其余, 重跑接着做), `--limit` 限制单次单表删除量 (默认 5000), 用 `ctid` 定位以适配不同主键。

**Jev 健康告警** — `typesafe.Client` 上装了 `HealthObserver`, 在 `Judge` 一个点插桩覆盖全部 7 个调用点, 复用 `PlatformAlert` 通道 (Redis 去重 15 分钟/键):

| 键 | 触发 |
|---|---|
| `jev-down` | 连续 3 次失败 (保活 45s ⇒ 约 2.5 分钟); 一次故障只告警一次 |
| `jev-recovered` | 恢复时清除去重键并通知, 下次故障可再报 |
| `jev-slow` | 调用**成功**但 ≥3s —— reply-path 预算是 guard 3s / route 4s, 这种"可达但无用"的调用产生与报错相同的静默降级 |

告警**只**在 `DeadlineExceeded` 时发, 不在 `Canceled` 时发 —— 后者是服务关闭或访客挂断, 与 Jev 健康无关, 报它会让运维很快无视告警。

## 5. 渠道 webhook 模式（internal/platform）

- Telegram: Platform 页保存 bot token → 后端调 `setWebhook`, URL=`PUBLIC_API_URL + /api/v1/...`; 部署改了域名必须同步改 `PUBLIC_API_URL` 并重新保存, 否则旧 webhook 地址继续往废 URL 推
- Meta/WhatsApp: `META_VERIFY_TOKEN` 用于回调 challenge 验证; Embedded Signup 需要 config_id
- 排障看 journalctl 中对应 webhook path 的请求行 (JSON 日志, 含 status/ip)

## 6. 前端构建事实

- `API_BASE` 构建时烘焙 (troubleshooting#9), 生产构建必带 `NEXT_PUBLIC_API_URL=https://<部署域名>/api/v1`
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
