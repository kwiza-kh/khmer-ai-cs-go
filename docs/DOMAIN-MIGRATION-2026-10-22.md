# 域名迁移记录：cs.wanfanginsulationmaterial.com → mekongchat.com（2026-10-22）

> 本文记录 2026-10-22 的域名迁移：做了什么、哪些外部控制台还需要更新、如何回滚。

## 现状

| 项 | 状态 |
|---|---|
| 主域名 | https://mekongchat.com（Cloudflare zone active，Free plan，Universal SSL 已签发） |
| www | https://www.mekongchat.com → 301 到 https://mekongchat.com |
| app 子域 | https://app.mekongchat.com 也指向同一应用（暂未重定向） |
| 旧域名 | https://cs.wanfanginsulationmaterial.com 继续完整服务（暂不重定向），用于已嵌入挂件、旧 webhook、旧 OAuth 回调与回滚 |
| 前端构建 | NEXT_PUBLIC_API_URL=https://mekongchat.com/api/v1 |
| 后端环境 | PUBLIC_API_URL / PUBLIC_FRONTEND_URL / ALLOWED_ORIGINS 已切到新域；SSO / Meta OAuth / Telegram 登录回调仍指旧域，等待外部控制台更新后切换 |
| 平台 Telegram bot | webhook 已切到 https://mekongchat.com/api/v1/webhook/telegram-platform（getWebhookInfo 已验证） |

## 本次已改动

- Cloudflare zone mekongchat.com：新增 A 记录 @ / www / app → 38.55.192.90（proxied）；always_use_https=on；min_tls_version=1.2；SSL=Full；TLS 1.3、HTTP/3、WebSockets、Brotli 开启。
- nginx：server_name 增加 mekongchat.com / www / app；新增 www → apex 301；旧域 server_name 保留。
- nginx CSP：主站与 /widget 的 connect-src 增加 https://mekongchat.com 与 wss://mekongchat.com，保证旧域前端在新 API 域下仍可用。
- 后端 .env-go：PUBLIC_API_URL、PUBLIC_FRONTEND_URL、ALLOWED_ORIGINS 更新；重启后端。
- 前端：用新 API 域名重新构建并发布（BUILD_ID 已换）；Cloudflare 两个 zone 全量 purge cache。
- 旧域名继续可用；密码重置链接、付款回跳、通知内会话链接、新生成的挂件嵌入代码现在都指向 mekongchat.com。

## 外部控制台待办（需要账号持有人操作）

1. Google Cloud Console → OAuth 客户端：Authorized redirect URI 增加 https://mekongchat.com/api/v1/auth/google/callback；Authorized JavaScript origins 增加 https://mekongchat.com。完成后把 SSO_REDIRECT_URL 改为新值、SSO_FRONTEND_URL=https://mekongchat.com/login。
2. BotFather → Telegram Login：Redirect URI 增加 https://mekongchat.com/api/v1/auth/telegram/callback。完成后把 TELEGRAM_LOGIN_REDIRECT_URL 改为新值、TELEGRAM_LOGIN_FRONTEND_URL=https://mekongchat.com/login。
3. Meta App 控制台：OAuth redirect 增加 https://mekongchat.com/api/v1/platforms/meta/oauth/callback；App Domains 增加 mekongchat.com；Webhook callback 改为 https://mekongchat.com/api/v1/webhook/meta（含 data-deletion 路由）；WhatsApp 同一 App 一并更新。完成后切换 META_OAUTH_REDIRECT_URL / META_OAUTH_FRONTEND_URL。
4. PayPal：为 https://mekongchat.com/api/v1/billing/paypal/webhook 新建 webhook（事件 PAYMENT.CAPTURE.COMPLETED），把新 id 写入 PAYPAL_WEBHOOK_ID 并重启；旧的 webhook 可先保留。
5. 渠道 webhook：已有租户的 Telegram / LINE / Zalo webhook 仍指旧域（旧域保持服务所以可用）；新连接会自动用新域；需要彻底迁移时按渠道重新验证/重装。
6. 已嵌入商家网站的挂件：旧代码继续可用（旧域保留），新生成的嵌入代码已指向新域；建议给商家发布公告后逐步替换。
7. 对外材料：邀请链接、二维码、Telegram/Facebook 简介、价格页、邮件签名等改为 mekongchat.com。
8. Cloudflare Email Sending（可选）：后续可在 mekongchat.com 上 Onboard Domain，用于密码重置邮件。

## 切换 SSO 的顺序（重要）

Google / Telegram / Meta 的 OAuth 回调与前端必须同域：新域登录页发起的 OAuth，其 state cookie 绑在新域 API 主机上，若回调仍落旧域会报 invalid_state。因此先让外部控制台接受新回调，再一次性切换对应 env（SSO_* / TELEGRAM_LOGIN_* / META_OAUTH_*）并重启；切换后旧域仅保留密码登录，等旧域页面 301 到新域后再停用旧域 SSO。

## 验证（迁移当天）

- https://mekongchat.com/、/login、/reset-password、/widget、/inbox 均 200；www 与 http 均 301 到 apex。
- /api/v1/auth/methods 正常 JSON；登录探测 401；密码重置 request 200 通用响应；WS 无 token 401、非法 Origin 403。
- CORS：新域与旧域 Origin 都返回对应 access-control-allow-origin。
- 旧域 https://cs.wanfanginsulationmaterial.com/ 仍 200；两域 /ready 由服务器本机 127.0.0.1:8081 确认。

## 回滚

- 后端 env 备份：/root/khmer-deploy/env-go.bak-20261022113619（恢复后 systemctl restart khmer-ai-cs-go）
- nginx 配置备份：/root/khmer-deploy/nginx-khmer-ai-cs.bak-20261022113531（恢复后 nginx -t && systemctl reload nginx）
- 前端备份：/opt/khmer-ai-cs/frontend-backup-20261022113735（换回后 restart khmer-ai-cs-web）
- Cloudflare：删除 mekongchat.com 的 A 记录即可让新域下线，旧域不受影响。
- 平台 bot：可用 Bot API setWebhook 改回旧域 URL。

## 安全提醒

迁移使用的 Cloudflare API Token 在对话中出现过，完成后应轮换；后续建议只创建 Zone:DNS Edit + Zone Settings Edit（限定 mekongchat.com）的 token。


## 2026-10-22 晚：平台 bot 与 Telegram 登录切换到新 bot

- 新 bot：@MekongChat_bot（bot id 8932862489，显示名 MekongChat），同时作为平台 bot 与 Telegram 登录（OIDC）用 bot。
- 服务器 env 已切：PLATFORM_TELEGRAM_BOT_TOKEN / PLATFORM_TELEGRAM_WEBHOOK_SECRET（新生成）/ PLATFORM_TELEGRAM_BOT_USERNAME=MekongChat_bot / TELEGRAM_LOGIN_CLIENT_ID / CLIENT_SECRET / REDIRECT_URL=https://mekongchat.com/api/v1/auth/telegram/callback / FRONTEND_URL=https://mekongchat.com/login；ADMINS 与 ALERT_CHAT 未变（838928815）。
- 新 bot webhook 已注册到 https://mekongchat.com/api/v1/webhook/telegram-platform，allowed_updates=[message, callback_query]；getWebhookInfo 回读通过。
- 旧平台 bot（旧品牌名，已停用）的 webhook 已删除；旧 bot 本身暂时保留，可之后在 BotFather 删除。
- 影响：管理员与已绑定商家需要先在 Telegram 打开 @MekongChat_bot 并点 Start，否则新 bot 无法主动发消息（实测 sendMessage 返回 chat not found）；已绑定行不需要改库，start 后即可恢复通知。目前只有 1 条绑定记录（user_id=1, chat_id=838928815）。
- Telegram 登录：新域 start 已返回 oauth.telegram.org 302（client_id、PKCE、新 redirect_uri 正确）；仍需在 BotFather Web Login 的 Redirect URIs 里确认包含新回调地址，并在新域实际登录一次验证。
- 回滚：env 备份见 /root/khmer-deploy/env-go.botbak-*（记录当时的旧 token/secret）；回滚时恢复 env + 重启，并用旧 bot token 重新 setWebhook 到旧域/新域对应 URL。

## 2026-10-23：SSO / OAuth 回调切换进展

> 顺序原则不变：**先让外部控制台接受新回调，再切 env 并重启**。先切 env 会让新域登录报 `invalid_state`/`redirect_uri_mismatch`。

| 项 | 状态 |
|---|---|
| Google SSO | ✅ **已切换**（2026-10-23）：Google 控制台已验证接受 `https://mekongchat.com/api/v1/auth/google/callback`，且旧域回调已从控制台移除（探测旧 redirect_uri 返回 `redirect_uri_mismatch`）。`.env-go` 的 `SSO_REDIRECT_URL` / `SSO_FRONTEND_URL` 已切到新域，env 备份 `env-go.bak-20261023060314` |
| Telegram 登录 | ✅ **已完成**（2026-10-23）：env 已是新域；新域 `/auth/telegram/start` → `oauth.telegram.org` 参数正确（client_id/PKCE/新 redirect_uri），`/auth/telegram/callback` 错误回跳新前端；账号持有人已确认 BotFather Web Login Redirect URIs 并**在新域真实登录成功** |
| Meta OAuth | ✅ **已切换**（2026-10-23）：Meta 控制台已登记新回调；`.env-go` 的 `META_OAUTH_REDIRECT_URL` / `META_OAUTH_FRONTEND_URL` 已切到新域，env 备份 `env-go.bak-meta-20261023060458`；实测见下 |

### Google 切换当天实测（2026-10-23）

- `/api/v1/auth/google/start` → 302 到 Google，`redirect_uri=https://mekongchat.com/api/v1/auth/google/callback`；Google 侧 302 到 `/v3/signin`（接受，无 mismatch）
- `/api/v1/auth/google/callback?code=<bogus>&state=<有效 state>` → 302 `https://mekongchat.com/login?google_error=failed`（**不是 `invalid_state`**，证明跨新域的 state + `sso_flow` cookie 绑定正常，只是假 code 换 token 失败）
- `/api/v1/auth/google/callback?code=bogus&state=bogus` → 302 `https://mekongchat.com/login?google_error=invalid_state`（错误回跳也落新前端）
- `/ready` = `{"service":"mekongchat",...,"version":"c03808b"}`；`/auth/methods` 仍 `google:true`

### Meta 切换当天实测（2026-10-23）

- `.env-go` 已切：`META_OAUTH_REDIRECT_URL=https://mekongchat.com/api/v1/platforms/meta/oauth/callback`、`META_OAUTH_FRONTEND_URL=https://mekongchat.com/platforms`（备份 `env-go.bak-meta-20261023060458`），重启后 `/ready` ok
- `POST /api/v1/platforms/meta/oauth/start`（管理员会话）→ 200，返回的 `authorize_url`：host=`www.facebook.com`、path=`/v24.0/dialog/oauth`、`redirect_uri=https://mekongchat.com/api/v1/platforms/meta/oauth/callback`
- `GET /api/v1/platforms/meta/oauth/callback?error=access_denied&state=bogus` → **303 `https://mekongchat.com/platforms?meta_oauth_error=invalid_state`**（新域与旧域入口都验证过，均回新前端）
- 完整「点连接 → Facebook 授权 → 选主页 → 完成」仍需商家账号带真实 Page 权限走一遍（无法用命令行替代）

### Meta 控制台操作清单（2026-10-23 账号持有人已完成）

1. developers.facebook.com → App id `2831499227197390` → **App settings → Basic**：App Domains 增加 `mekongchat.com` ✅
2. **Products → Facebook Login → Settings**：Valid OAuth Redirect URIs 增加 `https://mekongchat.com/api/v1/platforms/meta/oauth/callback`（旧域条目保留，便于回滚）✅
3. **Webhooks**：Callback URL 增加 `https://mekongchat.com/api/v1/webhook/meta`（含 `/meta/data-deletion`），verify token 不变（`META_VERIFY_TOKEN`）——待确认（不影响本次 OAuth 切换，租户级 webhook 仍走旧域且旧域在线）
4. WhatsApp / Instagram 同 App：Webhook 一并更新——待确认

### 仍待外部操作（非 SSO）

- PayPal：为 `https://mekongchat.com/api/v1/billing/paypal/webhook` 新建 webhook 并把 id 写入 `PAYPAL_WEBHOOK_ID`（当前仍是 sandbox）
- 各租户已配置的 Telegram/LINE/Zalo webhook 仍指旧域（旧域继续服务，可逐步迁移）
- 已嵌入商家网站的挂件仍加载旧域 `widget-embed.js`（旧域保留，逐步替换）
