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
| AI | `GEMINI_API_KEY` `GEMINI_MODEL`(gemini-3.5-flash) `GEMINI_FAST_MODEL` `GEMINI_MAX_TOKENS` `GEMINI_CACHE_TTL` `GEMINI_API_BASE`(可选) | **key 为空 = MOCK 模式** (模板回复, 演示/CI 用), 上线真 AI 必配; `GEMINI_API_BASE` 覆盖 REST 端点 (读 `apiBase+"/models"` 拼接), 现网指向 CF AI Gateway **含 /v1beta 后缀**: `https://gateway.ai.cloudflare.com/v1/<CLOUDFLARE_ACCOUNT_ID>/gemini-relay-gw/google-ai-studio/v1beta` (绕开 Google 对服务器区域的地域封锁, 2026-09-04 起)。坑: ①后缀丢了 → 网关 404 空 body; ②DB model_configs 的 key 含非标准字符也能用 (curl/Go 原样传); ③网关的 Authentication 必须 None, 否则 401 code 2009; ④请求日志在 CF 面板 AI→AI Gateway 可查; ⑤**生产实际模型由 DB `model_configs.is_default` 覆盖** (启动时 HotReload 日志 `Gemini configured from database model config`), 上面的 `GEMINI_MODEL` 只在没有 DB 配置时生效 —— 排查"模型不对"先看那行启动日志, 别只看 `.env-go`; ⑥**模型名必须锁定版本, 不能用 `-latest` 别名** (`gemini-flash-lite-latest` 在平台上根本不存在) 且平台**无 lite 档** (`gemini-2.5-flash-lite` / `gemini-3.5-flash-lite` 亚洲各区 404), 主模型与辅助模型统一 `gemini-3.5-flash`; **新模型名不是「不存在」而是区域作用域** —— `gemini-3.8-flash` 只在 `global` / `us` / `eu` 返回 200, 在每个单区域 (含 asia-southeast1) 是 404 (2026-09-25 实测, 矩阵见 11.6; 3.6/3.7 未重新实测) |
| Vertex (可选) | `GEMINI_PROVIDER`(**不设 = studio, 保持现状**) `GEMINI_VERTEX_PROJECT`(SA key 自带 project_id 时可省) `GEMINI_VERTEX_REGION`(默认 `asia-southeast1`, 实测可用区, 填错 = 每次调用 404) `GEMINI_VERTEX_SA_FILE` `GEMINI_VERTEX_API_BASE`(一般留空) | 平台端点 `{region}-aiplatform.googleapis.com/v1/projects/{p}/locations/{l}/publishers/google/models/{m}:generateContent`, 鉴权换成 **OAuth2 服务账号** (`cloud-platform` scope, 不再用 `x-goog-api-key`); 嵌入从 `:embedContent` 换成 `:predict` (须显式发 `outputDimensionality=768`, 否则默认 3072 与 `vector(768)` 不符)。**SA 密钥落位** `/opt/khmer-ai-cs/vertex-sa.json` + `chown khmerai:khmerai` + `chmod 600` (systemd 以 khmerai 跑, 权限不对即启动失败); ⚠️ **绝不提交进仓库** (`.gitignore` 无对应规则, 不兜底)。**切到 vertex 后 `GEMINI_API_BASE` 可以留空** —— 平台端点在区域内, 为绕地域封锁而生的 CF AI Gateway 中继不再需要 (摘掉这一跳是迁 Vertex 的收益之一; 回滚 studio 时再填回)。⚠️ 但**必须等彻底切完再清**: 它同时是**回滚通路**与 `embedcmp` 路径 A 的唯一前提, 清早了当天无症状 (见 §11.5)。`GEMINI_PROVIDER=vertex` 但缺 project/SA 时 **启动即退出** (`cmd/server/main.go` 的启动校验, 只读本地密钥不联网), 不是每轮请求才 500。TTS 在平台上**没有可用预览模型** → `TTS_ENABLED` 保持 false |
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

## 11. Vertex 切流（开发者视角）

切流执行本身是运维动作 (**命令级手册 = deploy-commands §10**), 但下面四件事只有改代码的人看得见, 也最容易被当成"运维的事"而漏掉。
实测数据与决策记录见 `docs/GEMINI-RATE-LIMIT.md` §十二（能不能迁）/ **§十四（怎么切、怎么验、怎么退）**。

### 11.1 模型名不是自由文本, 而是一份受平台约束的清单

- **锁版本, 不用 `-latest`**: 平台不认浮动别名 (它还会随上游漂移); 目标模型 = `gemini-3.5-flash`。
- **平台没有 lite 档**: `gemini-2.5-flash-lite` / `gemini-3.5-flash-lite` 亚洲各区全 404 ⇒ 主模型与 `FastModel` 统一 `gemini-3.5-flash`。两者同名后 `chatWithModel` 的"降级到快模型"会被 `fast != model` 守卫跳过 —— 这是设计如此, **不是回归**。
- `gemini-3.6/3.7/3.8-flash` **不是"平台上不存在"** —— 这是个曾被写进文档的错误结论, 它是"只在 AI Studio / 只在 asia-southeast1 量过"的产物。实测 (2026-09-25, 生产 SA): **`gemini-3.8-flash` 在 `global` / `us` / `eu` 返回 200, 在包括 asia-southeast1 在内的每一个单区域返回 404** ⇒ 新模型名是**区域作用域**的, 不是缺席的。3.6/3.7 没有重新实测, 不要再假设它们不存在; 用区域列表 (`vertexprobe -models <a,b,c>`, 或后台「模型」页的区域下拉) 按区域确认。完整矩阵见 11.6。
- **生产主模型来自 DB** (`model_configs.is_default`), **不是 `.env-go`**: 管理后台保存会 `HotReload` (无需重启), `psql` 直接 UPDATE **不会** ⇒ 用 SQL 改完必须重启, 否则就是"改了但没生效" (启动日志 `Gemini configured from database model config model=…` 是唯一权威信号)。
- **TTS**: `gemini-2.5-flash-preview-tts` 在所有测试区域**不可用** ⇒ `TTS_ENABLED` 保持 `false` (平台上没有可指向的 TTS 模型)。

### 11.2 嵌入的报文形状, 与那次"尺度下移"

- 平台嵌入走 `:predict` (**不是** `:embedContent` —— 后者在平台上 400), 且**必须显式发 `outputDimensionality=768`**: 不发就是模型默认 3072, 与 `vector(768)` 列不符。代码对非 768 的响应**直接报错**, 宁可失败也不写入错维向量。
- 平台的 `:predict` **不支持 task 条件化**: `gemini-embedding-001` / `text-embedding-005` / `text-multilingual-embedding-002` 三个模型 × 5 种拼写 (`parameters.task_type` / `taskType` × QUERY/DOCUMENT、`instances[].task_type`) **全部 200, 但向量与不带参数的基线逐位相同** (另有跨模型与假模型名双向对照)。所以迁移等于**丢掉查询/文档不对称** —— 这是既成事实, 不是可选项; 决策依据只能是实测。
- 实测 (租户 7, 真实 615 chunk + 45 条真实高棉语查询, 只读对比): 路径 B (Vertex 无条件) **不劣于** 路径 A (AI Studio 有 task) —— recall@5 `37→38`、recall@10 `40→41`、MRR `0.658→0.705`, 无一条低于 `RAG_SIMILARITY_FLOOR`。
  但**相似度尺度整体略降** (mean top1 `0.730→0.713`) ⇒ 这与排序无关, 却直接威胁绝对阈值 (见 11.4)。

### 11.3 三个只读工具（都别重复造）

| 工具 | 源 | 干什么 | 只读保证 |
|---|---|---|---|
| `vertexprobe` | `cmd/vertexprobe` | 平台可用性/能力**门禁**: token、聊天模型、嵌入维度; `-caps <model>` 跑六项请求形状; `-audiodir <dir>` 测音频容器 (ogg/m4a); `-tasktype` 测 task 条件化 | 只发探针请求; 自己建的 `cachedContents` 会删掉 |
| `embedcmp` | `cmd/embedcmp` | A/B 检索质量: 路径 A = studio+task 查询 vs **库里现存**文档向量; 路径 B = 两边都用 vertex 重算 | SELECT-only 守卫 + 会话 `default_transaction_read_only=on` + 无写入路径 (路径 B 的文档向量只在内存里) |
| `rageval` | `cmd/rageval` | 生产检索路径评测 + `-configs floor:ratio:skip` 扫描 | 只读 (`EvalRetrieval`) |

三个都在服务器 `/root/khmer-deploy/` 下跑 (SA key 与生产密钥不必离开那台机器)。
> **`vertexprobe -tasktype` 在本平台上必然 `exit 1`** —— "没有任何拼写能条件化嵌入"正是它要报的结论。它属于诊断, 写进发布门禁 = 每次发布都红。
> 跨工具别比数: `rageval` 的 dense 腿 SQL 截断在 `4×topK` 行, `embedcmp` 看得更深, 所以同一次运行两者的 MRR 差一点点 (`0.655` vs 路径 A 的 `0.658`) —— 已知口径差异, 各自与自己的基线比。

### 11.4 尺度下移为什么会打到生产闸门

生产与评测用的是同一套切点: `max(RAG_SIMILARITY_FLOOR, RAG_SIMILARITY_RATIO × 该查询最高相似度)`。
`ratio` **随尺度自适应**, `floor` **是绝对值** ⇒ 尺度整体下移时 `floor` 会更频繁地成为真正的切点, dense 腿可能被掏空, **而排序一点没变**。
另有两处绝对相似度判据会跟着轻微移动: `RAG_RERANK_SKIP` 现值, 以及 `service.go` 里写死的 `0.60` (`leaderClear` / `signalsAgree`)。
调之前先跑 `rageval -configs` 扫描拿证据, 选**能保住 dense 腿 recall 的最高 floor**; 复核流程见 deploy-commands §10.6 第 6 项。

### 11.5 换 provider **不会**重嵌入知识库（以及中继什么时候才能摘）

`SpawnIndexWorkers` 的重嵌入扫描只比较**模型名**:

```sql
-- embedding_model <> gemini.EmbeddingModel 才重排队; 两个 provider 上是同一个常量
UPDATE knowledge_documents SET index_status = 'pending'
 WHERE index_status = 'ready' AND embedding_model <> '' AND embedding_model <> 'gemini-embedding-001';
```

所以切到 vertex 后, 既有 chunk 仍是 AI Studio 产的向量, 只有**新查询**是 vertex 产的 —— 这个组合**没有被 embedcmp 覆盖**
(路径 B 是把文档向量也用 vertex 重算的; 工具注释明确把跨供应商配对排除在结论之外)。
- 要一致语料: 重新索引文档 (`POST /api/v1/knowledge/{id}/retry`, 或按上面 SQL 批量置 `pending`, 由 worker 重嵌入)。
  源文件/URL 已取不到的文档会变 `index_status='failed'`、`chunk_count=0`, **从密集检索里消失** ⇒ 先拿一篇试。
- `embedding_model` 列在两种情况下写的是**同一个字符串** ⇒ 从数据里看不出向量出自哪个 provider, 溯源自能靠操作记录。
- 顺带记住: `GEMINI_API_BASE` (CF AI Gateway 中继) 是这台机器到 AI Studio 的**唯一通路**, 也是**回滚**与 `embedcmp` 路径 A 的前提。
  它必须等**彻底切到 vertex 之后**再清, 而 vertex 路径根本不读它 ⇒ 清早了当天毫无症状 (详见 deploy-commands §10.5)。

### 11.6 区域化的模型目录（2026-09-25 起）

**为什么需要它**: 模型目录是**按区域**给的, 而"服务 3.8 的区域"(`global`) 不是生产跑的区域 (`asia-southeast1`) ⇒ 后台必须能**按区域查看**目录, 否则永远只能看到本区那几条。

**机制**: 后端不再用 gemini.go 里那份手写的 `vertexPublisherModels` 兜底列表 (它曾同时是"唯一来源"和"事实上的上限", 见 11.1)。现在直接问平台:

| 项 | 值 |
|---|---|
| 路由 | `GET {host}/v1beta1/publishers/google/models?pageSize=100` |
| 区域 host | 单区域 `{region}-aiplatform.googleapis.com`; `global` = `aiplatform.googleapis.com` (**不带 `global-` 前缀**, 那个主机名不解析) |
| 分页 | 响应带 `nextPageToken` 就继续取 (有页数上限; 截断会写进警告, 不假装完整) |
| 鉴权 | 与其它 vertex 调用同一套 SA bearer token (studio key 绝不上这条路) |

**后台接口** (平台管理员):

| 接口 | 说明 |
|---|---|
| `GET /api/v1/admin/models/{id}/available?region=<region>` | `region` 可省 —— 省略 = 服务端配置的区域 (`GEMINI_VERTEX_REGION`)。返回数组: `{name, display_name, launch_stage, capability, available}`; `capability ∈ chat/embedding/image/tts/live/other`; `launch_stage` 未知时为 `""` |
| `GET /api/v1/admin/models/vertex-regions` | `{regions:[{id,label}], current}`; 当前配置区域**排第一** (不在静态候选表里也会出现) |
| 响应头 `X-Model-List-Warning` | 列表不完整时 (区域 404 / 报错 / 返回空) 带上原因 (含区域名与 HTTP 状态); **同时**仍返回 200 + 能拿到的内容 + 配置中的在用模型 |

**⚠️ 三个必须记住的坑**

1. **`/v1/` 形式的列表路由 404, 只有 `/v1beta1/` 有用。** 之前"平台没有 publisher 列表路由"的结论就是在 `/v1` 上量出来的, 于是一份手写列表被当成了平台事实。
2. **列表不是"可调用性"判据 (最重要)。** 实测: `asia-southeast1` 只列 9 条、其中**没有 `gemini-3.5-flash`**, 而它在那个区域**可以调 (200)** —— 它正是生产的在用模型; 反过来 `us-central1` 的列表列了 3.x, 单区域 `generateContent` 却 404。所以接口的 `available` **默认为 `true`**, 列表成员关系**从不**被换算成"不可用"; 唯一权威是后台的「测试」按钮。谁把 `available` 改成"在列表里才为真", 谁就会把生产在用模型显示成不可用。
3. **区域选择器只作用于"列表", 不改服务路径。** 在用的模型与区域来自 `.env-go` 的 `GEMINI_VERTEX_REGION` + DB `model_configs.model_name`; 换区域看目录不会让流量换区, 也不会让目录内容拦住任何保存动作。

**实测的区域矩阵** (2026-09-25, 生产 SA; 200 = 可调, 404 = 该区域无此模型):

| 区域 | gemini-3.8-flash | gemini-3.5-flash | gemini-embedding-001 |
|---|---|---|---|
| `global` | 200 | 429 (配额) | 200 |
| `us` | 200 | 200 | 200 |
| `eu` | 200 | 200 | 200 |
| `asia-southeast1` ← 生产 | **404** | 200 | 200 |
| `asia-northeast1` | 404 | 200 | 200 |
| `asia-south1` | 404 | 200 | 200 |
| `us-central1` | 404 | **404** | 200 |
| `europe-west4` | 404 | **404** | 200 |

3.8 同样 404 的还有: `us-east1`、`us-east5`、`us-west1`、`europe-west1`、`europe-north1`、`asia-east1`、`northamerica-northeast1`、`australia-southeast1`。
⇒ 想上 3.8 只能把服务搬到 `global` / `us` / `eu`; 留在 `asia-southeast1` 就是 `gemini-3.5-flash` (3.5 在 `us-central1` / `europe-west4` 也是 404 ⇒ 换区域前连在用模型都要重测)。

### 11.7 为什么不把服务搬到 global 去用 3.8（2026-09-26 实测）

在真正切换之前先量了「换过去到底行不行」。方法: 从香港服务器用生产 SA 直连各区域,
每目标 8 次**完全相同**的真实高棉语客服请求 (4387 字符生产系统提示词,
`maxOutputTokens=2048`, 与线上一致), 串行执行:

| 目标 | 成功 | p50 | p95 | max | 输出 tok | 失败 |
|---|---|---|---|---|---|---|
| **asia-southeast1 / 3.5-flash（现网）** | **8/8** | **5.45s** | **7.51s** | **7.51s** | 114 | **无** |
| global / 3.5-flash | 8/8 | 5.76s | 8.75s | 8.75s | 118 | 无 |
| global / 3.8-flash | 7/8 | **22.51s** | 106.28s | 106.28s | 103 | 读超时 ×1 |
| us / 3.8-flash | 5/8 | 17.87s | 24.76s | 24.76s | 104 | 读超时 ×3 |
| eu / 3.8-flash | 5/8 | 21.38s | 103.24s | 103.24s | 91 | 读超时 ×3 |

**结论: 不切。** 3.8 的 p50 是现网的 **3-4 倍**, 并且 **22-38% 的请求直接超时**
(读超时 = 挂到 120s) —— 对一个聊天产品这不是「慢一点」, 是坏掉。
三个区域 (global/us/eu) 表现一致 ⇒ 是 3.8 本身在长系统提示词下的问题,
不是某个区域的问题, 换 `us` / `eu` 救不了。

两个容易看反的点:
- **不是 maxOutputTokens 的锅**: 各目标实际只产出 91-118 个输出 token, 是模型自己收敛的,
  没有撞上 2048 上限。把 cap 调小不会改善。
- **不是区域距离的锅**: global 上跑 **3.5** 是 5.76s / 0 超时, 而 global 上跑 **3.8** 是
  22.51s / 1 超时 ⇒ 差异来自模型, 不是香港到全球端点的那一跳。

现网真实流量佐证 (`/api/v1/chat` 的 200 响应, `latency_ms`): p50 **3.3s**、p95 4.2s、max 5.1s
—— 比上表的 5.45s 还快, 因为线上多数请求的 KB 上下文比这次构造的更短。所以上表是**偏保守**的
对比, 结论方向不受影响。

配额: 现网目标 12 次背靠背全绿 (`ok=12/12, 429=0`, 约 0.2 req/s 持续), 未触到限流;
`global` 端点在早前的探测里出现过 `429 RESOURCE_EXHAUSTED`, 加上上面 20s+ 的延迟,
即使哪天 3.8 的延迟修好了, 上 `global` 也要先确认配额。

复现: 基准脚本思路见本节方法段 (SA 直连 `:generateContent`, 勿经应用), 注意**必须带系统提示词**
—— 不带提示词量出来的延迟会显著偏低, 得出错误结论。

