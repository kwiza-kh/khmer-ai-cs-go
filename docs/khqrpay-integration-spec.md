# KHQRPay（khqr.cc / anajakpay.com）接入规格

> 本文件是本次接入的**唯一契约来源**。后端与前端各自实现时以本文为准；接口、字段名、
> 状态值不一致即视为 bug。上线前本文内容要合并进 `docs/DEVELOPMENT.md`。

## 一、这是什么

柬埔寨 KHQR（Bakong 网络的统一扫码标准）收款通道。资金**不经平台托管**，直接结算到商户
自己绑定的银行账户（ABA/ACLEDA/Wing 等）。对 MekongChat 来说它是 PayPal 之外的**第二个
一次性收款通道**：买 30 天付费套餐，语义与 `internal/paypal` 完全一致（不是订阅、不是自动
续费）。

## 二、合规风险（必须随代码一起交付，不得省略）

- 2026-09-11 柬埔寨国家银行（NBC）公开通告：`khqr.cc`（KHQRPay）**未获 NBC 许可或授权**
  提供支付相关服务，提醒商户谨慎使用。参见
  https://cambodiabusinesswatch.com/banking/khqrpay-nbc-warning/
- KHQRPay 自己的服务条款也写明：它不是 NBC 或 Bakong 的官方服务，只是独立运营的软件工具/网关。
- 因此本接入的硬性要求：
  1. **默认关闭**（`KHQRPAY_ENABLED=false`），运营显式打开才出现入口；
  2. **不得成为唯一支付通道**：PayPal 路径不得删除、不得降级；
  3. 前端在支付卡片上显示一行风险提示（三语），文案见 §六；
  4. 平台侧可随时用环境变量关停，不需要改代码或回滚发布。

## 三、厂商 API（已从公开页面取到的部分）

来源：https://anajakpay.com/khqr-api-cambodia 、
https://anajakpay.com/blog/how-to-accept-khqr-payments-nodejs 、
https://anajakpay.com/blog/bakong-api-full-guide （2026-10-26 抓取）。
官方详细文档 `https://anajakpay.com/docs/qr-api` / `/docs/api-v1` / `/docs/callback`
**需要商户账号登录**，本次拿不到正文。

### 1. 创建动态 KHQR（**已用真实凭据实测确认**，2026-10-26）

```
POST https://anajakpay.com/api/{profileId}/payment-gateway/v1/payments/qr-api
Content-Type: application/x-www-form-urlencoded

transaction_id=<我方订单号>      必填
amount=<USD 金额，最小 0.01>      必填
success_url=<客户付完跳回的地址>  必填
remark=<支付描述>                必填（**空串会被拒**）
items=<买了什么>                 选填（回调里原样返回；含 JSON/非 ASCII 要先 base64）
custom_fields=<自己的数据>       选填（JSON 要 base64；回调里已解码返回）
cancel_url=<客户放弃时去哪>      选填
hash=<签名>                      必填
```

签名：`hash = sha1_hex(secret + transaction_id + amount + success_url + remark)`
（按这个顺序直接拼接、无分隔符，取 SHA-1 十六进制小写；`remark` 必填且参与签名。）

> ⚠️ 营销页/博客里写的 `/…/payment-gateway/v1/payments/qr-api-khqr` 是**旧路径，在真网关 404**。
> 真路径有 `/api` 前缀、末尾是 `qr-api`。见 §3.1 实测。

响应（文档原文样例，**字段名是 `responseCode` / `responseMessage`**）：

```json
{ "responseCode": 0, "responseMessage": "Success",
  "data": { "transaction_id": "ORDER_1710500000", "amount": "5.00",
            "qr": "00020101021230620014…", "qr_url": "https://pay.khqr.cc/qr/…",
            "md5": "a1b2c3d4e5f6…", "req_time": 1710500000, "hash": "abc123hash…" } }
```

- **成功判据 = `responseCode == 0`**（JSON number）；错误信息在 `responseMessage`（实测 422 样例见 §3.1）。
- `data.qr` 是 KHQR 原始串，`data.qr_url` 是托管在 `pay.khqr.cc` 的二维码图片。
- 解析实现要同时认 `responseCode`/`responseMessage` 与旧的 `code`/`message`，并兼容 `data.*` 与 `data.data.*` 两种嵌套。

### 2. 支付结果通知（webhook，**有 sha256 签名**）

网关 POST `application/json` 到**「Global Webhook URL」（后台配置，优先）**，没配时回退到下单时的 `success_url`。报文：

```json
{ "req_time": 1710500000, "merchantBakongId": "merchant@wing",
  "transaction_id": "TOPUP_USER123_001", "amount": "5.00", "status": "SUCCESS",
  "items": "", "custom_fields": "{\"user_id\": 123, \"plan\": \"pro\"}",
  "payment_details": { "amount": "5.00", "currency": "USD", "sender": "customer@acleda",
                       "date": "Mar 09, 2026, 15:30:00", "externalRef": "FT24072H4F8G",
                       "bakonghash": "b8f9e1ca", "username": "Gateway User", "fullMsg": "…" },
  "hash": "a3f1c9e2b84d…" }
```

**验签（文档原话：Always verify this on your server before fulfilling any order）**：

```
hash = sha256_hex(secret + req_time + transaction_id + amount + "SUCCESS")
```

- `req_time` 是**整数**，拼签名时用十进制整数字面量；`amount` 用报文里的**原始字符串**（`"5.00"`，不要归一化成 `5`）。
- 回调的 `status` 恒为 `"SUCCESS"`（大小写不敏感处理）。
- **本接入要求验签通过才结算**（常量时间比较、缺 `hash` 也算失败、失败不触达开通）。路径令牌仍保留作纵深防御：令牌认证「调用方是网关」，sha256 认证「这笔钱确实付了」。

### 3. 核对端点（不需要）

文档提到的 "verification endpoint" 是给「只轮询、不收 webhook」的接法用的。既然回调本身有 sha256 签名，本接入**以签名为准**，不依赖额外的核对调用；`KHQRPAY_VERIFY_PATH` 保留为空（配置了也只是可选加固）。前端轮询只读本地 `payments` 行，不会因为轮询而开通任何东西。

### 3.1 实测（2026-10-26，用真实商户 profileId + secret）

**结论：真路径 = `https://anajakpay.com/api/{profileId}/payment-gateway/v1/payments/qr-api`，签名被接受；账户侧还缺 Bakong 令牌。**

| 请求 | 结果 |
|---|---|
| `POST https://khqr.cc/{profileId}/payment-gateway/v1/payments/qr-api-khqr`（营销页原样） | **404**（Laravel JSON 兜底：`The route … could not be found.`） |
| 同上但 host 换成 `anajakpay.com` | 404 |
| `/api/...`、`/api/v1/...` 前缀、profile 段换位（共 13 种组合） | 全部 404 |
| `GET https://anajakpay.com/api/user`（对照，验证探测方法有效） | **401 `{"message":"Unauthenticated."}`** ⇒ 路由表存在、判定方法没问题 |
| `POST https://anajakpay.com/api/{profileId}/payment-gateway/v1/payments/qr-api`（真路径 + 正确签名） | **422** `{"responseCode":1,"responseMessage":"Bakong Token Required: No active official Bakong OpenAPI token configured. Please add an official Bakong token in your dashboard settings to accept KHQR payments."}` ⇒ **路径与签名都对**，卡在账户配置 |

⇒ 运营侧待办：在 KHQRPay 后台补一个**可用的官方 Bakong OpenAPI 令牌**，否则下单永远 422（钱收不了，与我们的代码无关）。补齐后重打一笔 0.01 即可拿到 `qr`/`qr_url`。
| `api.` / `docs.` / `sandbox.` / `test.` 子域（khqr.cc 与 anajakpay.com） | 全部 DNS 不存在 |

其它已确认的事实：

- `https://khqr.cc/` 根路径 **301** 到 `https://anajakpay.com/`，但业务路径在两个域名上由**同一个 Laravel 应用**处理；
- 该应用对未匹配路由（`Accept: application/json`）返回 `{"message":"The route <path> could not be found."}` —— **会回显路径**，下次定位可直接看这条 message；
- 因此下单路径做成可配置：`KHQRPAY_CREATE_PATH`（见 §四.1），拿到文档后改 `.env-go` + 重启即可，不需要发版。

## 四、我方后端契约

### 1. 配置（`internal/config`，前缀 `KHQRPAY_`）

| 变量 | 默认 | 说明 |
|---|---|---|
| `KHQRPAY_ENABLED` | `false` | 总开关。false 时所有 KHQR 路由返回 503「通道未启用」 |
| `KHQRPAY_PROFILE_ID` | 空 | 商户 profile id（URL 路径段） |
| `KHQRPAY_SECRET` | 空 | 签名密钥（只在 env，绝不入库、绝不出现在日志/错误串） |
| `KHQRPAY_BASE_URL` | `https://anajakpay.com` | host 部分（`khqr.cc` 根会 301 到这里，业务路径同源） |
| `KHQRPAY_CREATE_PATH` | 空 | 空 = 内置模板 `/api/{profile}/payment-gateway/v1/payments/qr-api`（§三.1 实测的真路径）；非空 = 整条 path 由它决定，`{profile}` 会被替换成 URL 编码后的 profile id |
| `KHQRPAY_CURRENCY` | `USD` | KHQR 支持 USD/KHR；金额比较按币种精确匹配 |
| `KHQRPAY_PRICE_PRO` | 空 | 空 = 该套餐不通过 KHQR 售卖（与 PayPal 语义一致） |
| `KHQRPAY_PRICE_ENTERPRISE` | 空 | 同上 |
| `KHQRPAY_VERIFY_PATH` | 空 | 可选加固；回调本身有 sha256 签名，**不需要它也能安全结算**（§三.3） |
| `KHQRPAY_WEBHOOK_TOKEN` | 空 | **必填**（空 = 拒绝结算）。回调路径令牌，独立随机串，不得复用 `KHQRPAY_SECRET` |

`KHQRPAY_ENABLED=true` 但缺 `PROFILE_ID` 或 `SECRET` → 启动时 `PASS` 一条 warn（照
`validatePayPal` 的风格），运行期该通道视为未配置。

### 2. 数据

复用既有 `payments` 表（迁移 068，已有 `provider` 列），**不需要新迁移**：

- `provider = 'khqr'`
- `provider_order_id` = 我方生成的 `transaction_id`（格式 `MC-<userID>-<unix>-<4位随机大写字母数字>`，仅 `[A-Za-z0-9-]`，长度 ≤ 40）
- `provider_capture_id` = 厂商回传的交易号（有则写，没有就 NULL）
- `status`：`created` → `captured` / `failed`（与 PayPal 同一状态机）
- `detail` JSONB：`{"qr":"…","qr_url":"…","expires_at":"…","source":"khqr","raw":{…}}`

### 3. 路由（`internal/api/router.go`）

| 方法 | 路径 | 门禁 | 返回 |
|---|---|---|---|
| POST | `/api/v1/billing/khqr/order` | `tenantAdminOnly` | `{transaction_id, qr, qr_url, plan, amount, currency, expires_at}` |
| GET | `/api/v1/billing/khqr/{transaction_id}/status` | 登录 + 租户归属校验 | `{status, paid, paid_until?, current?}` |
| POST | `/api/v1/billing/khqr/webhook/{token}` | 公开，`a.rateLimit(120)`，路径令牌 `hmac.Equal` 校验（错→404，未配置→503） | `{"status":"ok"}` |

`GET .../status` 的状态取值（前端只认这四个）：`pending` | `paid` | `failed` | `expired`。
- 本地 `created` 且未过期 → `pending`
- 本地 `captured` → `paid`（并返回 `current`，同 `paidState`）
- 本地 `failed` → `failed`
- 本地 `created` 且已过期（`detail.expires_at`，缺省 15 分钟）→ `expired`

### 4. 开通（复用 PayPal 的那套幂等事务）

`activatePayment` 改成接收 provider 参数（`"paypal"` / `"khqr"`），其余不变：条件 UPDATE
决定谁开通、`amountMatches` 校验金额与币种、一个事务里「记落款 + 换套餐 + 延 paid_until」。
**不要复制第二份开通事务。**

### 5. 安全与失败方向

- 金额/币种以**本地订单行**为准，webhook 报文的 amount 只作比对，不一致 → 记 `failed` 并告警；
- **webhook 必须验签通过才结算**：`sha256(secret + req_time + transaction_id + amount + "SUCCESS")`，常量时间比较，缺 `hash` 视为失败；验签失败不触达开通（§三.2）；
- 路径令牌（`KHQRPAY_WEBHOOK_TOKEN`）保留作纵深防御：令牌认证「调用方是网关」，sha256 认证「这笔钱」。两者都不通过时按 §四.3 的语义返回，且不得落到「无校验也放行」；
- 缺失订单的 webhook（我们没建过的 transaction_id）→ 200 + `ignored`，避免厂商重试打爆；
- 密钥、`hash`、完整请求 URL 一律不得进日志（沿用 `stripURLErr` 的教训：错误串只留原因）。

### 6. 测试（无 DB 也要跑）

- 签名向量：给定 secret/tx/amount/url/remark → 固定 sha1 十六进制（自算一组钉死）；
- 响应解析：`data.data.*` 与 `data.*` 两种形态；
- 金额比对：`15.00` vs `15.0` vs `15`、币种大小写、KHR 与 USD 不互认（复用 `amountMatches` 的语义）；
- 下单参数组装：`remark` 缺省时签名仍一致；
- 路由/门禁的源码守卫（按仓库既有风格：扫描 handler 源码断言有 `tenantAdminOnly`）；
- 配置：`ENABLED=true` 但缺 profile/secret → `Enabled()` 为 false。

## 五、前端契约（`frontend/src/app/billing/page.tsx`）

- 套餐卡片的购买按钮：`billingCatalog` 返回里有 `khqr` 价格时才显示第二个按钮「扫码支付
  (KHQR)」（PayPal 按钮保持原样，两者并列）。
- 点击后：`POST /billing/khqr/order` → 打开弹窗显示 `qr_url` 图片（失败则用 `qr` 原始串
  兜底提示）、金额、`expires_at` 倒计时；每 3 秒 `GET .../status` 轮询，最多到过期。
- `paid` → 弹窗显示成功、刷新 `getBilling()`/套餐状态；`expired`/`failed` → 显示失败与
  「重新生成」按钮（重新下单）。
- 关闭弹窗即停止轮询（`useEffect` 清理）。
- 卡片底部风险提示文案键：`bl.khqrRisk`。
- 三语字典（`dict-zh.ts`/`dict-en.ts`/`dict-km.ts`）键集必须一致。

## 六、文案

| key | zh | en | km |
|---|---|---|---|
| `bl.khqrButton` | 扫码支付（KHQR） | Pay by QR (KHQR) | បង់តាម KHQR |
| `bl.khqrRisk` | 该通道由第三方 KHQRPay 提供；柬埔寨央行曾提示其未获许可，如需发票/对账请优先使用 PayPal。 | Provided by the third-party KHQRPay gateway; Cambodia's central bank has warned it is unlicensed — prefer PayPal if you need an invoice. | ច្រកនេះផ្តល់ដោយ KHQRPay (ភាគីទីបី)។ ធនាគារជាតិកម្ពុជាបានព្រមានថាមិនមានអាជ្ញាបណ្ណ — សូមប្រើ PayPal ប្រសិនបើត្រូវការវិក្កយបត្រ។ |
| `bl.khqrTitle` | 扫码支付 | Scan to pay | ស្កេនដើម្បីបង់ |
| `bl.khqrHint` | 用任意 Bakong 银行 App 扫码，付款后本页自动确认。 | Scan with any Bakong banking app; this page confirms automatically. | ស្កេនដោយកម្មវិធីធនាគារ Bakong ណាមួយ; ទំព័រនេះនឹងបញ្ជាក់ដោយស្វ័យប្រវត្តិ។ |
| `bl.khqrWaiting` | 等待付款… | Waiting for payment… | កំពុងរង់ចាំការបង់ប្រាក់… |
| `bl.khqrPaid` | 支付成功，套餐已开通 | Paid — plan activated | បានបង់ប្រាក់ — គម្រោងត្រូវបានដំណើរការ |
| `bl.khqrExpired` | 二维码已过期，请重新生成 | This QR expired — generate a new one | QR ផុតកំណត់ — សូមបង្កើតថ្មី |
| `bl.khqrFailed` | 支付未成功，请重试或联系管理员 | Payment did not complete — retry or contact support | ការបង់ប្រាក់មិនបានសម្រេច — សូមព្យាយាមម្តងទៀត |
| `bl.khqrRegenerate` | 重新生成 | Generate again | បង្កើតឡើងវិញ |
| `bl.khqrQrFallback` | 二维码图片加载失败，可在 Bakong 银行 App 中手动扫描或输入以下内容： | The QR image did not load — open any Bakong banking app and scan or enter this code: | រូបភាព QR មិនអាចផ្ទុកបាន — សូមបើកកម្មវិធីធនាគារ Bakong ហើយស្កេន ឬបញ្ចូលកូដនេះ៖ |
| `bl.khqrOnlyHint` | 仅支持扫码支付（KHQR） | QR payment (KHQR) only | អាចបង់បានតែតាម KHQR ប៉ុណ្ណោះ |
| `bl.checkoutKhqrOnly` | 当前仅扫码支付可用 | Only QR payment is available | មានតែការបង់ប្រាក់តាម KHQR ប៉ុណ្ណោះ |

> 后三个键是实现阶段补的：`bl.khqrQrFallback` 是"图片挂了用原始串兜底"的提示句；
> `bl.khqrOnlyHint` / `bl.checkoutKhqrOnly` 用于「PayPal 未配置但 KHQR 可卖」的部署，
> 避免卡片说「暂未开放购买」而旁边就有可用的扫码按钮。两条高棉语是实现者拟的，
> 上线前建议让母语者过一眼。

## 七、明确不做（本轮）

- 退款/争议（与 PayPal 同一个 P0-2 缺口，等统一处理）；
- 自动续费、发票、税费（P1-7）；
- 订阅回调/定期扣款（KHQR 本来就没有这个概念）；
- 真实生产商户号联调（等运营提供 `KHQRPAY_PROFILE_ID`/`KHQRPAY_SECRET`）。

## 八、上线步骤（运营侧）

1. 在 `https://anajakpay.com` 注册商户、绑定结算银行账户、建一个 test profile；
2. 把 profile id 与 payment secret 写进服务器 `.env-go`（`chmod 600`，不要进仓库）；
3. 先只开 `KHQRPAY_ENABLED=true` + 测试 profile，下一笔最小金额订单；
4. 核对：库内 `payments` 行 → webhook 到达 → `paid_until` 延长 30 天 → 前端弹窗变绿；
5. 通过后再换生产 profile；任何异常用 `KHQRPAY_ENABLED=false` 立即关停。
