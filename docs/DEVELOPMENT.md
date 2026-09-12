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
| `platform_configs.bot_token` | 商家 | 客服渠道（收发客户消息，**保留**） |
| `telegram_notify_settings.bot_token_enc` | — | **056 起废弃**，恒为 NULL，列保留只为二进制回滚不炸 |
| **`PLATFORM_TELEGRAM_BOT_TOKEN`** | **运营者** | **通知 / 登录 / 告警 / 命令台 / 支持 / 广播** |

**五个功能**：

1. **运维告警** —— 60 秒看门狗（DB/Redis）+ Gemini 配额耗尽。15 分钟去重（同一个 `key` 只报一次），恢复时清除告警键以便再次触发。

2. **商家账号绑定** —— 一键 deep link 取代「去 BotFather 建 bot、复制 token、粘贴、发消息、等轮询发现 chat_id」五步。`bot_token_enc` 为 NULL 表示「走平台 bot」，非 NULL 保持旧的每商家路径。

   ⚠️ **这条双路径已在 056 废除**，见下面 G 节。留下这段是为了说明当时为什么两套并存 —— 以及为什么那是错的。

3. **管理员命令台** —— `/status` `/tenants` `/tenant <id>` `/digest` `/broadcast` `/help`。**两道独立的门**：
   - Telegram user id 白名单（Telegram 自己断言 `from.id`）
   - 若该身份已绑定本地账号，该账号必须仍是启用的 `platform_admin` —— 这道门让「降权」真正生效，不用改白名单重新部署

   跨租户读取全部写 `audit_logs`。

4. **支持收件箱** —— 商家给 bot 发消息落 `platform_support_messages`。未知用户的消息也保留（可能是潜在客户）。

5. **双向中继** —— 商家消息推给运营者，**运营者直接长按那条推送回复**，bot 通过 `reply_to_message.message_id` 查 `platform_support_relay` 映射回给对应商家。响应 Telegram 的原生交互，不需要记 chat id 或输命令。

### E. 一个既有的生产 bug

排查中发现 `/admin/analytics/languages` 和 `/top-queries` 在生产上**每次调用都 500**（首次出现早于本轮任何改动）。

当时的判断是 `($2 || ' days')::interval` 有问题，全部 8 处改为 `make_interval(days => $2)`。

> **⚠️ 事后更正（2026-09-12，在生产库上实测）：当时的根因分析是错的。**
>
> 原判断称「Postgres 没有 `||(integer, text)` 操作符，绑定参数会在 prepare 阶段失败」。实测不成立：
>
> ```sql
> PREPARE b(int)    AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> PREPARE c(bigint) AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> PREPARE f         AS SELECT NOW() - ($1 || ' days')::interval;  -- PREPARE（通过）
> EXECUTE d(30);                                                  -- 2026-08-13（正确）
> ```
>
> int4、int8、以及不声明类型三种绑定方式**全部可用**。Postgres 从 8.3 起就有
> `anynonarray || text` / `text || anynonarray` 两个操作符，存在的意义正是让
> `整数 || 文本` 这种写法继续可用。
>
> **所以同一模式残留的 3 处不是 bug，不要再去「修」它们**：
> `internal/api/tasks.go:274`、`internal/api/tasks.go:292`（SLA 违约扫描）、
> `internal/rag/service.go:1032`（知识缺口）。三处的完整语句都已在生产库上
> prepare 通过。
>
> 改用 `make_interval` 本身没问题（更明确，也没什么代价），但**真实病因至今未知**
> —— 症状是真的，解释是错的。如果再出现同类 500，不要从这个方向找。

教训：**手测 SQL 用字面量会骗你，读到一条看似合理的根因也会。** 任何关于
「这个写法在 Postgres 上行不行」的结论，都该在真库上 prepare 一次再说。

### F. 其他

- **挂件高棉语文案** —— `frontend/src/app/widget/page.tsx` 的 `km` 语言槽填的是中文（与 `zh` 完全相同），高棉语访客在商家网站上看到全中文界面。这是流量最高的终端客户界面
- **`api.ts` 优先级 bug** —— `a || b + \`...\`` 里 `+` 优先级高于 `||`，`text` 非空时 HTTP 状态码被吞掉，502 网关页原样进 toast
- **路由命名空间** —— `/admin/business-hours` 等三个路由是租户自助设置（数据按 `user_id` 自作用域），挂在 `/admin/` 下误导人，且审计中间件会记录每个商家的日程编辑。移到 `/settings/`
- **命名空间清理** —— 11 个模式串残留（`/ready` 响应、TOTP `otpauth://` issuer、Telegram 测试消息等）

### G. Telegram 通知统一到平台 bot（056）

054 引入了平台 bot 一键绑定，但**刻意保留了每商家自带 bot 的旧路径**。两套并存意味着每个商家侧功能都要实现两遍 —— 而第二遍从来没做：内联的 接管/解决 按钮只接在每商家的 `getUpdates` 轮询上，走平台 bot 绑定的商家**按下按钮什么都不会发生**。

现在只剩一条路。

**代码侧的改动**

- **`telegram_notify_settings.bot_token_enc` 不再被读取**。`loadTelegramNotifyUncached` 直接用平台 bot token；平台 bot 没配就没有通知，没有回退
- **`ProcessNotifyCallbacks` 与 `tasks.go` 的 12 秒轮询整体删除**。它遍历 `telegram_notify_settings` 全表，对平台 bot 商家会拿平台 bot token 去 `getUpdates` —— 而平台 bot 注册了 webhook，Telegram 直接拒绝。迁移 054 的注释声称这次改动「移除了每租户轮询」，实际上循环还在，只是对不该跑的那批也在跑
- **按钮回调接进平台 bot webhook**（`handlePlatformCallback`）。顺带发现 webhook 里 `if upd.Message == nil { return }` 会把**所有** callback_query 静默丢弃 —— 这是按钮失效的真正原因
- **`/unlink`** —— 绑定此前是单向门，绑错号/换号/号被盗都无解。带内联确认按钮，因为误输命令就静默切断自己的通知是很糟的发现方式
- **机器人文案本地化**（`bot_texts.go`）—— 此前硬编码中文，而产品是高棉语优先。语言解析顺序：`users.language` 偏好 → Telegram 客户端的 `language_code` → 英语。**运营者命令台保持中文**，那个受众是 RelayChat 团队
- **API 面收窄** —— `PUT /settings/telegram-notify` 只接受三个开关；`/settings/telegram-notify/updates`（拉取会话列表）整个删除；不再有接受 bot token 或任意 chat_id 的端点

**存量商家必须重新绑定一次**，这是数据的性质决定的，不是实现选择：`chat_id` 是**和某个特定 bot 的会话**，只有那个 bot 能寻址。平台 bot 往商家与自己 bot 的 chat_id 发消息会 403（`bot can't initiate conversation with a user`）。没有可迁移的东西。

所以 056 把 `bot_token_enc` 和 `chat_id` 一起**清空**，而不是留着。留一个失效的 chat_id 比没有更糟 —— 设置页会显示「已连接」而投递一直静默失败。三个 `notify_*` 是偏好不是凭据，保留。

`bot_token_enc` **列本身不删**（可空，恒 NULL）：部署手册把「二进制回滚」当作受支持的操作，删列会让回滚到上一版时直接报列不存在，而不是优雅降级成「未配置」。

**Khmer 文案是初稿，发布前需要母语者过一遍** —— 写的是「能被看懂」而不是「地道」，而这是商家在手机上读的消息。

### H. SQL 对象引用审计 —— 查出 SLA 创建 100% 失败

`/tenants` 的列名错误暴露出一类**编译器完全看不见**的问题：SQL 字符串里引用的表 / 列 / 类型不存在。构造、vet、单测都不会报，只有真跑到那条查询才炸；如果调用方用 `_ =` 吞掉 error，还会表现为**静默的 0**。

于是写了个一次性工具（`sqlprobe`）做穷举：

1. 用 `go/ast` 解析全部 Go 源码，把字符串字面量按 `+` 拼接**还原成完整 SQL**
2. 逐条拿到**生产库**上 `PREPARE` —— 这正是当初暴露 `/tenants` 的机制
3. 另做一次「标识符差集」：SQL 里出现过的所有标识符 vs 生产库的表名/列名/类型名

结果（574 条候选）：

| 项 | 数 |
|---|---|
| 完整语句，prepare 通过 | 448 |
| prepare 失败 | 46 |
| 跳过（拼接残段 / format 串） | 80 |

46 条失败里，44 条是提取器误报（`VALUES (...)` 是 INSERT 被变量切断的尾巴、`DELETE /api/v1/...` 是 HTTP 路由不是 SQL），1 条提取不完整，**1 条是真的**：

> **`createSLA` 里的 `$6::sla_priority` —— 这个类型在全部 55 个迁移里从未出现过。**
>
> `sla_policies.priority` 实际是 `varchar DEFAULT 'normal'`，不是 enum。语句在 prepare 阶段就失败，
> 所以 **`POST /api/v1/sla` 从上线起 100% 返回「创建失败」**，一条策略都建不出来
> （生产库 `SELECT COUNT(*) FROM sla_policies` = 0，与此一致）。
>
> 修复：`$6::sla_priority` → `$6::varchar`。已在生产库上实测：prepare 通过，
> 实插一条返回真实 `sla_id`，回滚干净。

**顺带确认**：全仓所有 `::type` 强转里只有 `sla_priority` 指向不存在的类型；其余 4 个自定义类型（`platform_type`、`human_handoff_trigger`、`session_status`、`user_role`）生产库全部存在。

差集那一步另外列出 83 个「库里没有的标识符」，逐个核对后**全部是**单字母表别名、`AS` 输出别名、CTE 名、HTTP 路由片段、Go 变量和环境变量名 —— 无新增问题。

**这一步已经常驻成代码**：`backend-go/internal/sqlcheck/`。它做同样的事，且修掉了首版手工脚本的误报来源 —— 关键是**属于 `+` 拼接链的字面量只作为整条语句上报一次，绝不单独上报**。首版把每一段都单独上报，于是 `INSERT` 的尾巴 `VALUES (...) ON CONFLICT ...` 看起来像独立语句，但没有任何表可解析，prepare 只会报「列不存在」，与源码毫无关系。46 条失败里 44 条是这么来的。

常驻版本的实测结果（对生产库，454 条）：

```
checked 454 statements: 441 prepared, 11 skipped, 0 broken, 2 unparseable
```

2 条无法解析的是**真的残段**（被变量截断），作为提示打印、不判失败。**语法错和对象不存在被区别对待**：只有后者说明源码有问题，前者只说明提取器把残段当成了完整语句 —— 如果不区分，测试会因为噪声被忽略，那比没有测试更糟。

用法见「七、快速排查入口」。

---

## 三、迁移 050–056

| 版本 | 内容 | 注意 |
|---|---|---|
| 050 | 删除冗余 ivfflat 向量索引 | 001 建的，032 上 HNSW 后从未删 → 每个 chunk 写入维护两个索引。**普通 `DROP INDEX`（runner 在事务里跑，`CONCURRENTLY` 不可用），取 ACCESS EXCLUSIVE 锁** |
| 051 | `platform_configs.channel_identity` | LINE/Zalo 的 webhook 路由键。**自动学习**：连接时从 provider API 拉取；迁移前已连接的渠道在第一条**验签通过**的 webhook 上绑定（先验签再绑定——否则攻击者能用伪造的 destination 提前污染） |
| 052 | `users.token_version` + `user_totp.secret` 加宽 | 会话吊销机制。挂在鉴权中间件**已有的那次查询**上，零额外开销 |
| 053 | `users.email` 可空 + `telegram_sub` | Telegram 不给邮箱 |
| 054 | `telegram_notify_settings.bot_token_enc` 可空 + `linked_at` + `notify_announcements`；建 `platform_support_messages` | NULL token = 走平台 bot。**056 已废除这条双路径** |
| 055 | `platform_support_relay` | 中继映射。按 `(admin_chat_id, admin_message_id)` 唯一 —— Telegram 的 message id 只在会话内唯一 |
| 056 | 清空 `bot_token_enc` + `chat_id`，二者加注释 | **破坏性**：所有存量商家需重新绑定，见 G 节。列不删以保回滚 |

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
  --data-urlencode 'allowed_updates=["message","callback_query"]'
```

> ⚠️ **`callback_query` 不能漏。** `allowed_updates` 是白名单，Telegram 只投递列出的类型。
> 只写 `["message"]` 时，内联按钮（转人工的 接管/解决、/unlink 的确认）的按下事件**根本不会到达服务端**
> —— 而且失败是静默的：发送正常、按钮显示正常、按下去毫无反应，服务端连日志都没有。
> 2026-09-12 部署时线上正是漏的，已补。改完用 `getWebhookInfo` 回读确认。

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
| SQL 引用守卫需要 `DATABASE_URL` | `internal/sqlcheck` 已常驻（见 H 节与排查入口），**但只在设了 `DATABASE_URL` 时运行** —— 未设就 skip。常规 `go test ./...` 不会覆盖它，必须显式带着库跑一次，或接进部署流程。这是刻意的：检查只在「已应用迁移的库」上才有意义 |

---

## 七、快速排查入口

```bash
go test ./internal/migrations/     # 迁移对账 + 镜像同步
go test ./internal/redisstore/     # 限流器回归
go test -race ./...                # 竞态

# SQL 引用检查 —— 只在设了 DATABASE_URL 时运行,否则 skip。
# 它把源码里每条 SQL 拿去 PREPARE:只解析不执行,每条都在回滚的事务里,
# 因此可以直接指向生产库。发布前跑一次,能挡住「接口 100% 不可用」那类 bug。
cd backend-go
set -a; . ./.env-go; set +a          # 或 export DATABASE_URL=...
go test ./internal/sqlcheck/ -v

# 本机没有库时,用隧道把生产库映射过来(DSN 里的 5432 换成隧道端口):
#   ssh -f -N -L 15432:127.0.0.1:5432 root@<服务器>
#   DATABASE_URL=$(echo "$DATABASE_URL" | sed 's|:5432/|:15432/|') \
#     go test ./internal/sqlcheck/ -v

# 生产
curl -s http://127.0.0.1:8081/ready
journalctl -u khmer-ai-cs-go -f
psql "$DATABASE_URL" -c "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 5"
```

**镜像目录**：`backend-go/migrations/` 是供 psql 手查的副本，真源是 `internal/migrations/migrations/`。`TestMirrorDirectoryIsInSync` 会逐字节比对，忘了复制会在 `go test` 就暴露。
