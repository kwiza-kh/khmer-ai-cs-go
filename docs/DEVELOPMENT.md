# RelayChat 开发细节

面向接手/继续开发的工程师。记录**代码里看不出来的东西**：为什么这么设计、踩过什么坑、哪些约束不能破。

部署流程见 `deploy-khmer-ai-cs/`，不在这里重复。

---

## 一、先搞清几件容易被误导的事

### 1. 这个项目没有真正的多租户

`tenant_id` 在 Go 代码里出现 **0 次**。

**一个「租户」= 一行 `users`**。证据链：

- 迁移 021 的注释原文：「tenant-scoped settings … become per-user (**user_id**)」
- `tenant_overview` 是个对 `users` 做 rollup 的**视图**，不是表
- `listTenants` 的真实 SQL：`SELECT ... FROM users WHERE role <> 'platform_admin'`
- `tenantDetail(w, r, userID int32)` —— 租户 ID 就是用户 ID

所以 platform-admin 那个「租户管理」页面，本质是**用户管理 + 计费**。

子账号靠 `agent_teams(owner_user_id, agent_user_id)`，**一对一独占**：一个用户只能属于一个 owner 的团队。这条约束是 `addTeamAgent` 强制的，因为 `agent_teams` 同时充当了所有「外来 user_id」handler 的租户边界（见 `userInCallerTenant`）—— 如果允许一个用户被两个商家认领，A 商家就能借这条链管理 B 商家的账号。

### 2. 零 Row-Level Security

全部 55 个迁移里 `CREATE POLICY` 出现 **0 次**。隔离完全靠应用层手写 `WHERE user_id = $N`。

**这意味着任何一处漏写 WHERE 就是跨租户泄漏，没有数据库兜底。**

`chat_messages` 和 `knowledge_chunks` 连 owner 列都没有，要靠 join `sessions` / `knowledge_documents` 绕上去 —— 漏写的风险面比表面上更大。

### 3. 技术栈的三个常见误记

| 记成 | 实际 |
|---|---|
| Gin | **stdlib `net/http` ServeMux**。`GIN_MODE` 只是残留的 config 字段 |
| sqlx | **pgx/v5** |
| Radix UI | **Base UI**（`@base-ui/react`）+ shadcn `base-nova` |

### 4. 迁移是前向单行道

`schema_migrations(version)` **只记版本号，无校验和、无 down**。

- 已应用的迁移文件**永远不要改内容** —— 改了不会报错，但新库和老库 schema 会分叉
- 向后兼容是硬约束：只能加表/加列，少删

---

## 二、本轮实现（2026-09-12）

### A. 安全与稳定性修复（10 项）

| # | 问题 | 修复 |
|---|---|---|
| 1 | **限流器永久封禁** | `redisstore/redis.go` 每次检查无条件 `Expire` 重置 TTL → 活跃用户窗口永不关闭，越过上限后永久 429。改为 Lua 脚本原子「自增 + 仅在键无 TTL 时补」。配 4 个回归测试（已用「改回旧逻辑」验证过测试确实会挂） |
| 2 | **跨租户越权** | `listUsers` 全局无过滤；`updateUserRole` 可改任意非 platform_admin 用户；`assignSession` 可指派给外租户；`addTeamAgent` 可认领他人用户；`assignRole`/`unassignRole` 无归属校验。全部加租户边界 |
| 3 | **LINE/Zalo 无租户路由** | 解析器取「第一个 active 配置」，第二个商家永远收不到消息。改为按 `channel_identity` 路由 + 自动学习（详见迁移 051） |
| 4 | **静默 mock 降级** | Gemini 任何失败都返回模板回复且 `err=nil`，管道照常投递给真客户。改为「未配置」与「配置了但失败」区分，后者返回错误触发有界重试 |
| 5 | **`EnsureReplyWindow` 丢消息** | 把数据库查询错误当成「无入站上下文」→ 返回终态 `PolicyError` 不重试。改为区分 `pgx.ErrNoRows` 与瞬时错误 |
| 6 | **出站乱序** | `claimOutbound` 缺「同会话存在更早 pending」守卫（入站一直有）。补上 |
| 7 | **通知被静默丢弃** | 计费累加与转人工通知和 LLM 分类共用 8 槽信号量，尖峰时被丢。拆成 `criticalSem`(64, 不丢) 与 `classifySem`(8, 可丢) |
| 8 | **知识编译丢数据** | 先删旧的编译子文档、再调 LLM，失败即丢失旧摘要。改为成功后在单事务里换；并挪出索引 worker（原来会阻塞整个索引队列最长 60 秒） |
| 9 | **成本统计盲区** | 只有 4 处调用 `usage.Record`。加 `gemini.AuxUsageObserver` 钩子覆盖编译/翻译/重排/改写/转写/图片描述；上下文用 `usage.WithUser` 携带租户 |
| 10 | **认证加固** | TOTP 密钥加密存储；`token_version` 会话吊销；重新注册 2FA 需密码；API key 默认一年过期 |

### B. RelayChat 改名

原名「Khmer AI 客服系统」。改名涉及 18 个文件，其中**两处做了平滑过渡**：

**JWT issuer**（`auth.go`）—— 直接改会让所有在线用户当场登出：
```go
JWTIssuer       = "relaychat"      // 新 token
legacyJWTIssuer = "khmer-ai-cs"    // 旧 token 仍接受
```
配了 `TestTokenIssuer` 三个断言。**旧 issuer 的接受逻辑在 24 小时后（一个 JWT 生命周期）可删**。

**WebSocket 子协议**（`realtime.go` + `lib/realtime.ts`）—— 前后端各写一份，不同步改实时功能就断。服务端 `Subprotocols` 列表和 token 提取都认两个值，缓存旧 JS 的浏览器也能正常升级。

**刻意没改的**：Go module 名（40 处 import，零用户收益）、`R2_BUCKET` 默认值（真基础设施名）、`InsecureDefaultJwtSecret`（安全绊线常量，改了就检测不到那个弱密钥）。

### C. Telegram 登录（OIDC）

新增 `internal/api/telegram_sso.go`，复刻 `google_sso.go` 的三段式，**多一层 PKCE (S256)**。

**与 Google 的关键差异**：Telegram 不给邮箱。所以：
- 身份只认 `sub`（数字 id），**绝不认 `@username`** —— 后者可改、可回收，拿它做键就是「改个名字登进别人账号」
- 用户名合成：优先 `@username`，没有或已被占用则退到 `tg_<sub>`
- `email` 放开为可空（迁移 053），**不合成假邮箱**污染用户列表和 CSV 导出

**顺手修的一个坑**：`email` 改可空会让所有 `Scan(&email)` 到非指针 `string` 的地方在遇到 NULL 时炸掉（Postgres 的 NULL 扫进 Go 的 `string` 是运行时错误，不是零值）。排查出 5 处并加了 `COALESCE(email,'')`。其中 `profile_handlers.go` 最阴——它对任何错误都返回 `ErrNotFound("用户不存在")`，Telegram 用户打开个人资料页会看到莫名其妙的报错。

### D. 平台级 Telegram bot

**这是本轮最大的一块**。一个 operator 自有的 bot，与所有租户级 bot 区分开：

| Bot | 归属 | 用途 |
|---|---|---|
| `platform_configs.bot_token` | 商家 | 客服渠道 |
| `telegram_notify_settings.bot_token_enc` | 商家 | 通知 |
| **`PLATFORM_TELEGRAM_BOT_TOKEN`** | **运营者** | **登录 / 告警 / 命令台 / 支持 / 广播** |

**五个功能**：

1. **运维告警** —— 60 秒看门狗（DB/Redis）+ Gemini 配额耗尽。15 分钟去重（同一个 `key` 只报一次），恢复时清除告警键以便再次触发。

2. **商家账号绑定** —— 一键 deep link 取代「去 BotFather 建 bot、复制 token、粘贴、发消息、等轮询发现 chat_id」五步。`bot_token_enc` 为 NULL 表示「走平台 bot」，非 NULL 保持旧的每商家路径，**完全向后兼容**。

3. **管理员命令台** —— `/status` `/tenants` `/tenant <id>` `/digest` `/broadcast` `/help`。**两道独立的门**：
   - Telegram user id 白名单（Telegram 自己断言 `from.id`）
   - 若该身份已绑定本地账号，该账号必须仍是启用的 `platform_admin` —— 这道门让「降权」真正生效，不用改白名单重新部署

   跨租户读取全部写 `audit_logs`。

4. **支持收件箱** —— 商家给 bot 发消息落 `platform_support_messages`。未知用户的消息也保留（可能是潜在客户）。

5. **双向中继** —— 商家消息推给运营者，**运营者直接长按那条推送回复**，bot 通过 `reply_to_message.message_id` 查 `platform_support_relay` 映射回给对应商家。响应 Telegram 的原生交互，不需要记 chat id 或输命令。

### E. 一个既有的生产 bug

排查中发现 `/admin/analytics/languages` 和 `/top-queries` 在生产上**每次调用都 500**（首次出现早于本轮任何改动）。

根因：`($2 || ' days')::interval`。`daysParam` 返回 `int`，pgx 按 int4 绑定，而 **Postgres 没有 `||(integer, text)` 操作符** → 查询在准备阶段失败。

阴险之处：**用字面量手测是好的** —— `30 || ' days'` 里 `30` 被推断成 unknown 类型，走的是 `||(text,text)`。只有绑定参数才炸。第一次手测我差点误判成「SQL 没问题」。

同一模式在该文件有 **8 处**，只有 2 个接口被点到所以只暴露了 2 个。全部改为 `make_interval(days => $2)`。

### F. 其他

- **挂件高棉语文案** —— `frontend/src/app/widget/page.tsx` 的 `km` 语言槽填的是中文（与 `zh` 完全相同），高棉语访客在商家网站上看到全中文界面。这是流量最高的终端客户界面
- **`api.ts` 优先级 bug** —— `a || b + \`...\`` 里 `+` 优先级高于 `||`，`text` 非空时 HTTP 状态码被吞掉，502 网关页原样进 toast
- **路由命名空间** —— `/admin/business-hours` 等三个路由是租户自助设置（数据按 `user_id` 自作用域），挂在 `/admin/` 下误导人，且审计中间件会记录每个商家的日程编辑。移到 `/settings/`
- **命名空间清理** —— 11 个模式串残留（`/ready` 响应、TOTP `otpauth://` issuer、Telegram 测试消息等）

---

## 三、迁移 050–055

| 版本 | 内容 | 注意 |
|---|---|---|
| 050 | 删除冗余 ivfflat 向量索引 | 001 建的，032 上 HNSW 后从未删 → 每个 chunk 写入维护两个索引。**普通 `DROP INDEX`（runner 在事务里跑，`CONCURRENTLY` 不可用），取 ACCESS EXCLUSIVE 锁** |
| 051 | `platform_configs.channel_identity` | LINE/Zalo 的 webhook 路由键。**自动学习**：连接时从 provider API 拉取；迁移前已连接的渠道在第一条**验签通过**的 webhook 上绑定（先验签再绑定——否则攻击者能用伪造的 destination 提前污染） |
| 052 | `users.token_version` + `user_totp.secret` 加宽 | 会话吊销机制。挂在鉴权中间件**已有的那次查询**上，零额外开销 |
| 053 | `users.email` 可空 + `telegram_sub` | Telegram 不给邮箱 |
| 054 | `telegram_notify_settings.bot_token_enc` 可空 + `linked_at` + `notify_announcements`；建 `platform_support_messages` | NULL token = 走平台 bot |
| 055 | `platform_support_relay` | 中继映射。按 `(admin_chat_id, admin_message_id)` 唯一 —— Telegram 的 message id 只在会话内唯一 |

---

## 四、新增环境变量

### Telegram 登录

```bash
TELEGRAM_LOGIN_ENABLED=true
TELEGRAM_LOGIN_CLIENT_ID=          # BotFather → Login Widget / Web Login
TELEGRAM_LOGIN_CLIENT_SECRET=      # ⚠️ 不是 bot token，是单独发的 secret
TELEGRAM_LOGIN_REDIRECT_URL=https://<域名>/api/v1/auth/telegram/callback
TELEGRAM_LOGIN_FRONTEND_URL=https://<域名>/login
TELEGRAM_LOGIN_ALLOW_SIGNUP=true
TELEGRAM_LOGIN_REQUEST_PHONE=true  # 申请 phone scope，拿 Telegram 已验证手机号
```

### 平台 bot

```bash
PLATFORM_TELEGRAM_BOT_TOKEN=       # 平台凭据，按 JWT secret 级别对待
PLATFORM_TELEGRAM_WEBHOOK_SECRET=  # 随机串，Telegram 回传用于验签
PLATFORM_TELEGRAM_ADMINS=          # 逗号分隔的 Telegram 数字 user id
PLATFORM_TELEGRAM_ALERT_CHAT=      # 告警投递目标，缺省 = 第一个 admin id
PLATFORM_TELEGRAM_BOT_USERNAME=    # @handle（不带 @），用于生成绑定 deep link
```

**BotFather 配置要点**：
- 用 **mini app**（不是聊天窗口）→ 选中 bot → `Login Widget` → **Switch to OpenID Connect Login**
- **Redirect URIs 必须精确匹配**，Telegram 只认注册过的地址
- Client Secret 与 bot token 是**两个不同的东西**

**Webhook 注册**（部署后执行一次）：
```bash
curl -X POST "https://api.telegram.org/bot${PLATFORM_TELEGRAM_BOT_TOKEN}/setWebhook" \
  --data-urlencode "url=https://<域名>/api/v1/webhook/telegram-platform" \
  --data-urlencode "secret_token=${PLATFORM_TELEGRAM_WEBHOOK_SECRET}" \
  --data-urlencode 'allowed_updates=["message"]'
```

---

## 五、给后来者的几条经验

**1. `ON CONFLICT DO UPDATE` 的陷阱**
`VALUES` 里的值**只在 INSERT 分支生效**。绑定功能第一版就栽在这：`bot_token_enc = NULL` 只写在 VALUES 里，冲突分支没清 —— 结果**已配过自己 bot 的商家（正是这功能最该服务的对象）绑定后毫无变化**，而全新商家是好的。所以它「测试通过、上线失效」。

写完 `ON CONFLICT` 一定要**两条分支都验**。

**2. 手测 SQL 用字面量会骗你**
参数化查询和字面量查询的**类型推断不一样**（见那个 analytics bug）。要验证参数化查询，就得用真实的绑定路径，或者直接看日志里生产环境的实际报错。

**3. 改可空列之前先搜扫描点**
`Scan(&x)` 到非指针 `string` 遇到 NULL 是**运行时错误**，而且经常被 `if err != nil { continue }` 吞掉，表现为「数据静默消失」。

**4. Tailwind v4 的任意值类不生成**
`text-[#2AABEE]` 这类**不会生成 CSS**（项目历史上栽过，commit `8c6ca15`）。品牌色要用注册好的 `@theme` 令牌（`text-brand-telegram` 等，见 `globals.css`）。

**5. 前端 AGENTS.md**
`frontend/AGENTS.md` 要求写前端代码前先读 `node_modules/next/dist/docs/`。Next 16 的破坏性变更集中在 turbopack 配置、`middleware`→`proxy`、`next lint` 迁移。

---

## 六、已知缺口（未实现）

| 项 | 说明 |
|---|---|
| 出站投递失败激增告警 | 其他告警信号都接了，这条没接 |
| 前端 11 个零消费者导出 | `streamChat` / `streamWidgetChat` / `chatVoice` 等，是现成的 API 客户端层，非缺陷 |
| `/widget` 自带 SSE 解析器 | 未复用 `streamWidgetChat`；重构客户-facing 聊天路径为零收益换风险 |
| 邮件渠道 | `config.go` 有 SMTP 字段，**零代码** |
| 域名 | 仍是 `cs.wanfanginsulationmaterial.com`（保温材料公司子域）。换域名要同步改：DNS、Cloudflare、nginx、BotFather Redirect URIs、Meta/Google OAuth 回调，**并且已嵌出的挂件会全部失效** |
| 平台 bot 单点 | token 泄漏影响所有商家（旧架构是每商家隔离）。这是换掉每商家自建 bot 的代价 |

---

## 七、快速排查入口

```bash
go test ./internal/migrations/     # 迁移对账 + 镜像同步
go test ./internal/redisstore/     # 限流器回归
go test -race ./...                # 竞态

# 生产
curl -s http://127.0.0.1:8081/ready
journalctl -u khmer-ai-cs-go -f
psql "$DATABASE_URL" -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5"
```

**镜像目录**：`backend-go/migrations/` 是供 psql 手查的副本，真源是 `internal/migrations/migrations/`。`TestMirrorDirectoryIsInSync` 会逐字节比对，忘了复制会在 `go test` 就暴露。
