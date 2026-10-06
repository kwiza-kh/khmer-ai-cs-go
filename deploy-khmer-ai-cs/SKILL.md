---
name: deploy-khmer-ai-cs
description: 将「Khmer AI 客服系统 (khmer-ai-cs-go, Go 后端 + Next.js 前端)」部署/更新到远程 Linux 服务器, 经 Cloudflare 子域名 <部署域名> 提供 HTTPS。当用户提到 部署 khmer-ai-cs / 高棉语 AI 客服 / server-go / migrate-go / <部署域名> / 连接 <部署服务器IP> / 更新线上版本 / 跑迁移 / nginx 反代 / systemd 服务 / Cloudflare 521 / HTTPS 异常 时触发。
---

# Khmer AI 客服系统 (khmer-ai-cs-go) 部署与迭代

将「Khmer AI 客服系统」Go 后端 + Next.js 前端部署/更新到远程 Linux 服务器 (经 Cloudflare 子域名 HTTPS), 覆盖首次全量部署与日常迭代发布 (后端二进制、前端 standalone、数据库迁移、回滚)。

## 何时触发

- 部署/更新 khmer-ai-cs 客服系统到服务器、上线、发布新版本
- 提到 <部署服务器IP> 或 <部署域名>
- 跑迁移 / migrate-go / schema_migrations / pgvector
- nginx / systemd / Cloudflare / 521 / HTTPS 异常排查
- 前端 standalone 发布、Telegram/Meta webhook 域名配置

## 架构与环境（先读这个）

```
浏览器 ──HTTPS──> Cloudflare (橙色云朵, zone=<域名>)
                      │ HTTPS:443 (源站自签证书, SSL 模式必须 Full)
                      ▼
              nginx (<部署域名>)
                ├── /api/                    ──> 127.0.0.1:8081  server-go (Go 后端, SSE 关 buffering)
                ├── /api/v1/realtime/inbox   ──> 127.0.0.1:8081  WebSocket 升级 (3600s 超时)
                └── /                        ──> 127.0.0.1:3001  next-server (standalone)
                                                    Postgres 17 (127.0.0.1:5432, db=khmer_ai_cs) + Redis (127.0.0.1:6379)
```

| 项 | 值 |
|---|---|
| 服务器 | <部署服务器IP> (Debian 13 trixie, x86_64) — **与 WMS 系统同机共存, 勿动 wms.service** |
| SSH | root + 密码 (从环境变量 `KHMER_SSH_PASSWORD` 读取, **勿把明文写进文件/仓库**; 密码值放密码管理器) |
| 域名 | **cs**.<域名> (Cloudflare A 记录 → <部署服务器IP>, 橙色云朵代理) |
| 后端 | Go (go.mod 声明 go 1.26; 构建机 go 越新越好 —— 2026-09-29 核对的线上二进制由 go1.27.1 构建) `server-go` → :8081, 二进制内 `/health` `/ready`; **服务器上没装 Go, 二进制在构建机交叉编译后上传** |
| 前端 | Next.js 16 standalone (`node server.js`) → 127.0.0.1:3001; `API_BASE` 在**构建时**烘焙 (见下) |
| 数据库 | 系统级 Postgres 17 (apt, 非 Docker), 扩展 **pgvector 0.8.0 + pg_trgm**, 迁移表 `schema_migrations` (见 `internal/migrations/migrations/` 当前文件数) |
| 缓存 | 系统级 Redis (apt, requirepass), 会话/缓存/Gemini 结果缓存 |
| 运行用户 | `khmerai` (系统用户), 应用目录 `/opt/khmer-ai-cs` |
| systemd | `khmer-ai-cs-go.service` (现行 Go 后端, env=`.env-go`) / `khmer-ai-cs-web.service` (前端) / `khmer-ai-cs.service` (**旧后端, 已 disable, 留作回滚**) |
| HTTPS | Cloudflare 托管证书 (浏览器侧) + 源站自签 `/etc/nginx/ssl/wms.crt` (与 WMS 共用); zone SSL 模式必须 **Full** |

## 与 WMS 部署的关键差异（不要照搬经验）

| 点 | WMS | 本项目 |
|---|---|---|
| 编译位置 | 服务器上装 Rust 编译 | **本机/构建机交叉编译 Go** (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`), 服务器无 Go |
| 前端 | Vite SPA dist, 同源相对路径 | **Next standalone**, `NEXT_PUBLIC_API_URL` 构建时烘焙, 忘设 → 线上打 localhost 挂 |
| 数据库 | SQLite 单文件 | Postgres 17 + pgvector + Redis (系统服务, 独立于部署目录) |
| SSL 模式 | Flexible 或 Full 均可 | **必须 Full** (源站 :80 会 301 到 https, Flexible 会形成重定向环) |
| 部署目录 | /opt/wms | /opt/khmer-ai-cs |

## 部署工作流（迭代发布）

0. **先问一句“线上那个二进制到底是从哪棵树构建的”** —— 版本号不一致不一定是“线上落后”，也可能是**线上有仓库里没有的代码**（它不以任何方式报错，只能在这一步发现）：
   ```bash
   strings -a /opt/khmer-ai-cs/server-go | grep -oE "vcs\.(revision|modified|time)=[^ ]*" | sort -u
   git -C <仓库> cat-file -t <上面的 revision>        # fatal: Not a valid object = 这个提交从未进过仓库/origin
   ```
   拿它跟本地 `git log` 对：对不上就**先别编译**，去构建树（通常是那台 macOS）把缺的提交推上来。
   2026-09-27 实例：线上 `9333fb1…` + `vcs.modified=false`（干净树构建）但本地与 origin **都没有这个对象**，
   而且二进制里有 `embed keep-warm: connection established` 等三条字面量、仓库 grep 不到 ——
   **直接部署本地 HEAD 会把线上的 embed keep-warm 默默删掉**。发布因此暂停。
   补充：`/ready` 的 `version` 是构建期 ldflags 注的短哈希，它只告诉你“哪个 commit”，不告诉你“那些 commit 在不在仓库里”。
1. **构建后端** (本地, 需 go ≥1.26, `brew install go`): `backend-go/` 下交叉编译 `server-go` 与 `migrate-go`; 发布前跑一次 SQL 引用检查 (`go test ./internal/sqlcheck/`, 需 `DATABASE_URL`, 未设会 skip) 质量门 —— 它挡的是编译器看不见的那类 bug (命令见 references/deploy-commands.md §1)
2. **构建前端**: `NEXT_PUBLIC_API_URL=https://<部署域名>/api/v1 npm run build`, 组装 standalone + `.next/static` + `public` 打 tar (§2)
3. **上传**: scp 到 `root@$KHMER_DEPLOY_HOST:/root/khmer-deploy/` (§3)
4. **后端发布**: 备份旧二进制 → `systemctl stop khmer-ai-cs-go` → (有迁移则) 跑 `migrate-go` → cp 新二进制 → `chown khmerai` → start (§4; 先 stop 再 cp, 否则 Text file busy)
   - **迁移跑完、新二进制 start 之前, 强制**跑一次 SQL 引用检查 —— 这是本工作流里唯一一次"库已是新 schema"的窗口:
     老 schema + 老二进制是自洽的, 而新二进制引用新列时, 编译器/单测都看不见 (历史事故正是这类: 列被代码引用但没迁移)。
     ```bash
     cd backend-go
     DATABASE_URL=postgres://khmerai:...@127.0.0.1:5432/khmer_ai_cs \
       SQLCHECK_REQUIRED=1 go test -count=1 ./internal/sqlcheck/
     ```
     `SQLCHECK_REQUIRED=1` 把"没有 DATABASE_URL 就 skip"变成**硬失败**: 不加它, 忘了设 DSN 时这个门禁会静默通过 (`PREPARE` 只解析不执行, 指生产库是安全的)。
     跑不通过就**不要** start 新二进制 —— 回到上一步修 SQL 或补迁移。
5. **前端发布**: `cp -a frontend frontend-backup-<ts>` → 解包新目录 → `chown -R khmerai` → `systemctl restart khmer-ai-cs-web` (§5)
6. **验证**: 服务器 `/ready` 双检查 + 本地走域名 `POST /api/v1/auth/login` 有 JSON 响应 (§6)
7. **回滚**: 后端 = 还原备份二进制 + restart; 前端 = 换回 backup 目录 (§7)

## SSH 连接（macOS, 无 sshpass 的机制）

```bash
mkdir -p /tmp/khmer-deploy
cat > /tmp/khmer-deploy/askpass.sh <<'EOF'
#!/bin/bash
echo "${KHMER_SSH_PASSWORD:-}"
EOF
chmod +x /tmp/khmer-deploy/askpass.sh
export KHMER_SSH_PASSWORD='<root 密码, 从密码管理器取>'
export KHMER_DEPLOY_HOST='<部署服务器IP, 从密码管理器取>'

# sshrun 封装 (下文所有远程操作用它; 2>&1 后过滤 known_hosts 噪音)
sshrun() {
  DISPLAY=:0 SSH_ASKPASS=/tmp/khmer-deploy/askpass.sh SSH_ASKPASS_REQUIRE=force \
  ssh -o StrictHostKeyChecking=accept-new \
      -o ConnectTimeout=20 -o NumberOfPasswordPrompts=1 -o PubkeyAuthentication=no \
      root@$KHMER_DEPLOY_HOST "$@" 2>&1 | grep -v "Warning: Permanently added"
}
```

- macOS 的 OpenSSH 直接支持 `SSH_ASKPASS_REQUIRE=force` (**不需要 setsid**, 那是 Linux 特有的)。2026-09-27 实测：**Windows OpenSSH 10.3p1 (Git Bash) 同样可用**，不需要 sshpass/plink。
- ⚠️ `sshrun`/`scprun` 把输出过了 `grep -v` 管道：**scp 成功时没有任何输出，grep 退 1** —— 用 `&&` 链下一步会把“上传成功”误判成失败。用 `;` 分段，或下一步自己 `[ -f 远端文件 ]` + `sha256sum` 对账（上传后比对校验和本来就是必做的，二进制没有其它完整性信号）。
- 复杂远程命令 (引号嵌套/heredoc) 一律 **base64 传参执行**: `B64=$(echo "<脚本>" | base64); sshrun "echo $B64 | base64 -d | bash"`, 避免转义地狱
- **⚠️ 远程命令里不要用 `pkill -f "<模式>"`** — 会匹配并杀掉当前 ssh 会话自身; 清进程用 `pgrep` 拿 PID 再精确 kill
- **⚠️ 上面这套 askpass 是“没密钥时的退路”，不是默认。先试密钥，它能省掉整段密码流程：**
  ```bash
  ssh -o BatchMode=yes -o ConnectTimeout=15 root@$KHMER_DEPLOY_HOST 'echo KEY_AUTH_OK; hostname'
  ```
  2026-09-27 实测：这台机器 `~/.ssh` 里早就有可用密钥、`BatchMode` 一次就登上，而下面两个 wrapper 里的
  `-o PubkeyAuthentication=no` 反而把可用的密钥认证**主动关掉了**，天天逼着从环境变量递明文密码。
  所以：**先跑上面那行，能过就全程用裸 `ssh`/`scp`（或去掉那个选项的 wrapper）**；只有它报
  `Permission denied (publickey)` 才回落到 askpass。
- ⚠️ 密码只在确实需要时才 `export`，**不要落盘**。另：`sshrun`/`scprun` 末尾的 `grep -v` 在“无输出”时退 1，
  用 `&&` 链下一步会把成功当失败（scp 成功本来就没输出）——用 `;` 分段，上传后按 sha256 对账。

## 核心配置模板

**systemd** `/etc/systemd/system/khmer-ai-cs-go.service` (现行)：
```ini
[Service]
User=khmerai
WorkingDirectory=/opt/khmer-ai-cs
EnvironmentFile=/opt/khmer-ai-cs/.env-go
ExecStart=/opt/khmer-ai-cs/server-go
Restart=on-failure
RestartSec=3
```
前端 unit 用 `Environment=PORT=3001` `Environment=HOSTNAME=127.0.0.1` + `ExecStart=/usr/bin/node server.js` (Workdir=`/opt/khmer-ai-cs/frontend`)。

**nginx** `/etc/nginx/sites-enabled/khmer-ai-cs` 三个 location (完整文件见 references/deploy-commands.md §8):
- `location /api/` → 8081, **`proxy_buffering off`** (chat/stream SSE 必需)
- `location /api/v1/realtime/inbox` → 8081, WebSocket Upgrade 头 + `proxy_read/send_timeout 3600s` (**必须排在 /api/ 之前**)
- `location /` → 3001 (Next standalone), 安全头含 CSP (`connect.facebook.net` 允许 Meta SDK; `media-src https:` 允许 R2 音频回放), 80 块 `return 301 https://`

## Vertex 区域与在用模型（生产已切完：`vertex` + `global`）

**现行状态（2026-09-26 迁 Vertex；下面三个值 2026-09-29 核对）**: `.env-go` 有 `GEMINI_PROVIDER=vertex`、区域 = `global`，
**主模型 = `gemini-3.8-flash`**（DB `model_configs.is_default` 行）、**快模型 = `gemini-3.8-flash`**（`GEMINI_FAST_MODEL`，2026-09-28 起；它比 3.5-flash 快约 1.5×）、
嵌入 = `gemini-embedding-001` @ 768 维；`GEMINI_API_BASE` 已清空（不再走 CF AI Gateway 中继）。
**采样温度 = 1.0**（`model_configs.temperature`，2026-09-29 接通）：该值现在随每次对话请求发出，管理页改它即时生效；
越界（[0,2] 之外）会被写入接口拒绝（否则会让这个配置的每一次回复都 400）；启动日志会打印实际值，
`"temperature":"platform default"` 表示不发送、用平台默认。四档实测（unset/1.0/0.7/0.3 × 3 轮 × 24 例）无可测量差别，故取厂商推荐值 1.0 —— 细节见 docs/DEVELOPMENT.md §十三。
下面的“两步切换”仍是手册 —— 只不过现在要切的不是“要不要上平台”，而是**换模型名/换区域**。

| 变量 | 值 / 要点 |
|---|---|
| `GEMINI_PROVIDER` | 现行 = `vertex`；`vertex` = 平台端点 + OAuth2 服务账号。**拼错的值只会当 studio**（不会悄悄把生产改道），所以改完必须核对启动日志与 `token_usage.model` 两个信号 |
| `GEMINI_VERTEX_PROJECT` | GCP 项目 id。SA key 自带 `project_id` 时可省（缺了才报错） |
| `GEMINI_VERTEX_REGION` | **现行 = `global`**，而且是**硬依赖**：`gemini-3.8-flash`（主与快模型自 2026-09-28 起同名）在 `asia-southeast1` 是 404，**改回单区域 = 主链路与快链路同时挂**。代码里的默认 `asia-southeast1` 只是未显式配置时的兜底；控制台可改服务区域（写入 DB），但改之前必须确认在用模型在新区域可调 |
| `GEMINI_VERTEX_SA_FILE` | `/opt/khmer-ai-cs/vertex-sa.json` |
| `GEMINI_VERTEX_API_BASE` | 一般**留空**（区域已决定端点）; 仅中继/测试时才覆盖, 末尾不带 `/` |

- **SA 密钥落位**: 上传到 `/opt/khmer-ai-cs/vertex-sa.json` 后 `chown khmerai:khmerai` + `chmod 600`（systemd 以 `khmerai` 运行, 权限不对就是启动失败）。
- ⚠️ **绝不提交进仓库**: 仓库 `.gitignore` 里**没有**对应规则, 别指望它兜底; 密钥只存在于服务器 `/opt/khmer-ai-cs/`。
- 切到 vertex 后 `GEMINI_API_BASE` **可以留空**: 平台端点在区域内, 当初为绕 Google 地域封锁才加的 **Cloudflare AI Gateway 中继不再需要** —— 摘掉这一跳正是迁 Vertex 的收益之一（回滚到 studio 时再填回来）。
  - ⚠️ **时序**: 必须等**彻底切到 vertex（且不再需要回滚/对照）之后**再清。中继是这台机器到 AI Studio 的**唯一通路**, 提前清 = 把**回滚路径**和 `embedcmp` 路径 A 一起废掉; 而 vertex 路径**根本不读这个变量**, 所以清早了当天毫无症状, 等你真需要它时才发现没有降落伞（deploy-commands §10.5）。
- `GEMINI_PROVIDER=vertex` 而 project/SA 缺失时 **启动即退出**（`main.go` 的启动校验）, 不再是每轮请求才 500; 校验只读本地密钥文件, 不联网。
- 模型名在配置上**锁定具名版本**（现行 `gemini-3.8-flash`，主与快模型同一个名字）。“不要用 `-latest`”的理由**不是平台不认** —— 2026-09-27 实测 `gemini-flash-lite-latest` 在 `global` 返 200；真实理由是它随上游漂移、发布不可复现。“平台没有 lite 档”同样已被推翻：`gemini-3.5-flash-lite` 在 `global` 是 200（但已不是生产在用值，快模型 2026-09-28 换成了 `gemini-3.8-flash`）。**但“能不能调”按区域量，区域错 = 每次 404。**
- ⚠️ 新模型名是**区域作用域**的, 不是"不存在": 实测 (2026-09-25, 生产 SA; 2026-09-27 复测) `gemini-3.8-flash` 在 `global` / `us` / `eu` 返回 200, 在**每一个单区域（含 `asia-southeast1`——就是 2026-09-26 之前的产区）返回 404**; `gemini-3.5-flash` 在 `us-central1` / `europe-west4` 也是 404。后台「模型」页有**区域下拉**可按区查看目录；**切服务区域是旁边的第二个动作**（2026-09-29 起：写 DB `model_configs.vertex_region`，不重启不改 env，写入前用在用模型探一次新区域，`NOT_FOUND` 就拒绝）—— 机制、接口与完整实测矩阵见 references/dev-guide.md §11.6。

### 切流顺序（两步, 可分离）

**发布门禁（切到 vertex 之后每次发布都跑）**：在改任何生产配置之前先证明目标区域仍然
提供我们依赖的能力。必需项失败会以 `exit 1` 退出，直接中断发布：

```bash
/root/khmer-deploy/vertexprobe -sa /opt/khmer-ai-cs/vertex-sa.json \
  -project gen-lang-client-0354228918 -region global -timeout 45s
echo "EXIT=${PIPESTATUS[0]}"   # 退出码才是结论；不要拿管道尾部的状态当退出码
# 期望: "All required checks passed." + 退出码 0
# 必需项 = OAuth 令牌签发 / `gemini-3.8-flash`（主与快模型同名，默认只需验它一个）可用 / 嵌入返 768 维
# 新版默认已对齐生产 (region=global, -require 就是那两个名字); 想看其它模型用 `-models`，它删不掉 `-require`
```

> ⚠️ **2026-09-27 之前的 `vertexprobe` 跑 `-region global` 必然假红**：它自己拼 `global-aiplatform.googleapis.com`
> 这个不服务的主机名（global 的 host 不带区域前缀），对每个模型都回一坨 HTML 404，看起来像“global 什么都不提供”。
> 分清新旧：`./vertexprobe -h 2>&1 | grep -- -require` 有输出才是新版；旧版在服务器上备份为
> `/root/khmer-deploy/vertexprobe.pre-global-fix-20260927`。

三个**假通过陷阱**（都会让门禁看起来是绿的）：
- `-tasktype` 在本平台**必然 exit 1** —— 那是"平台不支持 task 条件化"的诊断结论，
  不是故障；把它放进任何门禁都会常红。
- `-audiodir` 指向空目录会打印 `0/0` 并 **exit 0**。要肉眼看输出里有 ogg 与 m4a 行。
- 把 `… | tail -40; echo $?` 的 `$?` 当退出码 —— 那是 `tail` 的。用 `${PIPESTATUS[0]}` 或不走管道。


| 步 | 动作 | 为什么能分开 |
|---|---|---|
| **1** | 把 DB `model_configs.is_default` 那行的 `model_name` 改掉（**不是改 `.env-go`** —— 线上主模型来自 DB）, 在当前传输层上观察 | 先换名字后换区域（或反之），两步任一步出问题能一眼归因；回滚只是把名字改回去。旧文档这一步选的是 `gemini-3.5-flash`，因为它在 AI Studio 侧也返 200 ⇒ 没有"新名字没人认"的窗口 |
| **2** | `.env-go` 改 `GEMINI_PROVIDER=vertex` + `GEMINI_VERTEX_PROJECT` / `_REGION`（现行 `global`）/ `_SA_FILE`, 重启 | 回滚 = 改回/删掉 `GEMINI_PROVIDER`（**不设就是 studio**）+ 重启，一条变量。**顺序陷阱**：区域与模型必须同步——先把区域改回单区域而不先把模型名改回 = 两边同时 404 |

- **切流前门禁**: `vertexprobe`（必需项失败即 `exit 1`, 不许进第 2 步）; **切流后验证**: 六项能力 `-caps` / 音频容器 `-audiodir` / `rageval` 复现基线 / 复核 `RAG_SIMILARITY_FLOOR`（相似度尺度整体下移, 而它是绝对值）/ 429 是否消失 —— **确切命令、预期输出与失败判据见 `references/deploy-commands.md` §10**（含收尾清理与"决定不迁"时该删什么）。
- ⚠️ **别拿 `vertexprobe -tasktype` 当门禁**: 平台上"没有任何拼写能条件化嵌入"就是实测结论, 它**必然 `exit 1`** —— 那是诊断工具要报的结果, 不是故障。
- ⚠️ **换 provider 不会自动重嵌入知识库**（重嵌入扫描只认**模型名**, 而 `gemini-embedding-001` 在两个 provider 上同名）⇒ 切流后要么重建一次文档、要么立刻用 `rageval` 实测确认, 别假设等于对照实验里的路径 B（详见 deploy-commands §10.1）。

## 收款（PayPal）

| 项 | 值 / 要点 |
|---|---|
| 变量 | `PAYPAL_CLIENT_ID` / `_SECRET` / `_MODE`(`sandbox` 或 `live`) / `_CURRENCY` / `_PRICE_PRO` / `_PRICE_ENTERPRISE` / `_WEBHOOK_ID`；**缺 webhook id 不阻止启动**（只拒“id/secret 只配一半”和非法 mode），所以缺了是静默的 |
| 建 webhook | **用应用凭据调 API 建（应用锚定）**：`POST https://api-m.sandbox.paypal.com/v1/notifications/webhooks`，正文 `{"url":"https://cs.<域名>/api/v1/billing/paypal/webhook","event_types":[{"name":"PAYMENT.CAPTURE.COMPLETED"}]}` → 把返回的 id 写进 `.env-go` 重启。dashboard「Add webhook」建的是 **account 锚定**，与应用凭据不同域：验签**必然** `FAILURE`，事件在 `webhooks-events` 里也查不到；它上面的「Send test」同样测不到应用锚定那条 |
| 验证 | 只有真实付款能证明：`POST /billing/paypal/capture` 200 后几秒内 journal 出现 `/api/v1/billing/paypal/webhook` **200** 且无 `paypal webhook rejected`。已验证时 `/billing` 卡片会显示“有效期至”；`payments.detail.source` 是 `capture` 还是 `webhook` 能看出哪条路先到 |
| 续费/到期 | `paid_until` 从**原到期日**顺延 30 天（不从付款日重算）；用量周期（`cycle_end`）不跟着动；到期由 `expirePaidPlans` 降回 free；退款事件**不处理**（手工改） |
| 回滚 | 收款是追加式的：回滚二进制不会坏 `payments` / `tenant_billing.paid_until` |

## 验证清单

```bash
# 服务器本机 (绕过 nginx 直连后端)
curl -s http://127.0.0.1:8081/ready    # {"service":"relaychat","status":"ok","checks":{"database":true,"redis":true},"version":"<git-sha>"}
# ⚠️ service 是 "relaychat", 不是 "khmer-ai-cs" (internal/api/auth_handlers.go 里写死)。
# ⚠️ version 是构建期注入的 git 短哈希 (ldflags -X ...api.Version), 不是固定字符串 ——
#    部署后拿它核对"线上跑的到底是哪个构建", 这是唯一的权威信号。
curl -s -o /dev/null -w "%{http_code}\n" http://127.0.0.1:3001/    # 200
systemctl is-active khmer-ai-cs-go khmer-ai-cs-web                 # active active

# 本地 (走 Cloudflare 全链路)。⚠️ 域名上没有 /health —— nginx 只转 /api/, 裸路径全是 Next 页面
curl -s -X POST https://<部署域名>/api/v1/auth/login \
     -H "Content-Type: application/json" -d '{"username":"x","password":"***"}' -o /dev/null -w "%{http_code}\n"  # 401 = API 链路通
curl -sI https://<部署域名>/ | grep -iE "content-security|HTTP"                            # CSP 头
```

journalctl 里周期性 `/api/v1/realtime/inbox 401 WARN` = 未带 token 的 WS 重试探测, 属正常噪音, 不是故障。

## 故障排查速查

| 症状 | 原因 | 解法 |
|---|---|---|
| HTTPS 521 | Cloudflare 连不上源站 | zone SSL 必须 Full; `cs` A 记录必须 <部署服务器IP> (不能是 CF 边缘 IP) |
| Gemini 502 / `User location is not supported` | Google 按出口 IP 地域封锁 **AI Studio** 端点 (2026-09-04 起, 服务器区域被拒)。**生产已走 Vertex, 不再命中这一条** —— 只有在回滚到 studio 时才会重现 | 回滚 studio 时必须把中继填回: `.env-go` 的 `GEMINI_API_BASE=https://gateway.ai.cloudflare.com/v1/<acc>/gemini-relay-gw/google-ai-studio/**v1beta**` — **`/v1beta` 后缀绝不能丢** (gemini.go 用 `apiBase+"/models"` 直接拼接, 丢了 = 网关 404 空 body)。CF Worker 边缘中继无效 (出口同被识别为受限区域)。现行状态 = **Vertex 区域内端点, `GEMINI_API_BASE` 留空**（清理时序见 §10.5：中继同时是回滚路径与 `embedcmp` 路径 A 的唯一前提）|
| 模型列表 200 但为空 | gemini.go ListModels 曾按 `data` 字段解析, Google 实际返回 `models` | 已修复 (2026-09-04); 若回归先查此解析 |
| 模型列表 404 `no longer available to new users` | 测试用了退役模型名 | 用 DB `model_configs.model_name` 里配的现役模型 (当前 `gemini-3.8-flash`)。⚠️ “`-latest` 平台不认”与“没有 lite 档”都是**单区域结论**：2026-09-27 实测两者在 `global` 都返 200 —— 但配置仍然只准用具名版本（漂移与可比性）。"新名字不存在"也是错的: `gemini-3.8-flash` **存在**，只是在 `global`/`us`/`eu` 可调、在每个单区域 404 (`gemini-3.6/3.7` 未重新实测) —— 按区域查后台「模型」页的区域下拉, 别再用"存不存在"下结论 (dev-guide §11.6) |
| Vertex 切完**启动即退出** (`systemctl` = failed) | `GEMINI_PROVIDER=vertex` 但 project/SA 缺失、路径错或 `khmerai` 读不到 key → `main.go` 启动校验 `os.Exit(1)`（只读本地文件, 不联网） | 看 journal 的 `invalid Gemini provider configuration` 一行; **先回滚 provider 把服务拉起来**再修 (deploy-commands §10.4/§10.7) |
| Vertex 业务请求 404 | 模型名与区域不匹配（名称是区域作用域的：`gemini-3.8-flash` **只在 `global`/`us`/`eu` 有**，单区域 404）, 或区域填错 | 生产 = `global` + `gemini-3.8-flash`（主/快同名）；**改区域前先确认在用模型在新区域可调**（控制台切换带探测守卫）；探针 `-require`/`-caps` 先验证；按区域查后台「模型」页的区域下拉 (dev-guide §11.6) |
| 门禁 `-region global` 全红 | 手上的 `vertexprobe` 是 2026-09-27 之前的版（自己拼 `global-aiplatform...` 主机名）| `./vertexprobe -h \| grep -- -require` 无输出 = 旧版；本机 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o vertexprobe ./cmd/vertexprobe` 重编后上传（先备份旧的）|
| 启动日志的 `model=` 与现状不符 | 那行只反映**启动那一刻**的 DB 值；后台保存走 `HotReload` 就地改运行时模型，不再打日志 | 以 `token_usage.model`（只在成功出话时写）或「测试」按钮为准；`model_configs.updated_at` **没有触发器**，不能当修改时间（dev-guide §11.1）|
| Vertex 切完检索命中变少 | 相似度尺度整体下移 (mean top1 `0.730→0.713`), 而 `RAG_SIMILARITY_FLOOR` 是**绝对值**; 也可能知识库没重嵌入 (换 provider 不触发重嵌入) | `rageval` 对基线 → 按 deploy-commands §10.6 第 6 项用 sweep 复核 floor; 重嵌入见 §10.1 |
| 清掉 `GEMINI_API_BASE` 后回滚失败 | 中继是这台机器到 AI Studio 的**唯一通路**, 清早了 = 没有降落伞 (vertex 路径不读它, 所以当天无症状) | 把中继地址填回 `.env-go` 再回滚 (deploy-commands §10.5) |
| 浏览器无限重定向 | SSL 模式被改成 Flexible (源站 :80 有 301) | CF 改回 Full |
| 页面能开但接口全挂 | 前端构建忘设 `NEXT_PUBLIC_API_URL` (默认烘焙 localhost:8080) | 带正确 env 重新 build + 发布前端 |
| 回答全是模板/无 AI | `GEMINI_API_KEY` 为空 = MOCK 模式 (设计如此) | 需要真 AI 就配 key 重启 |
| `Text file busy` | 服务运行中覆盖二进制 | `systemctl stop khmer-ai-cs-go` 后再 cp |
| migrate 报 DATABASE_URL is required | `migrate-go` 用 godotenv 只读 `./.env` (旧 Rust 配置), 且 CWD 不对 | 先 `set -a; . ./.env-go; set +a` 再跑 (Load 不覆盖已有变量), 见 deploy-commands §4 |
| 迁移后启动崩 | 迁移 SQL 失败但记了 schema_migrations? 不会 (单事务回滚) — 多半是列被代码引用但未迁移 | `migrate-go --status` 对账; 必要时 `--mark-applied` |
| SSH banner exchange 超时 | 连续密码错误被临时封 / 服务器负载 | 等 30~60s 再试, 别反复重连 |
| 密码被拒 Permission denied | 少打了末尾的点 | 密码结尾是**两个点** |
| 大文件上传 413 | 该 server 块没配 `client_max_body_size` (默认 1m) | cs 的 443 块按需加大后 `nginx -s reload` |
| Telegram webhook 注册失败 | `PUBLIC_API_URL` 不对或没走 https | 必须 `https://<部署域名>` (不带 /api/v1) |
| PayPal 投递每次都 `verification failed (FAILURE)` | webhook 建在 dashboard（account 锚定）→ 与应用凭据不同域，验签无从通过 | 用应用凭据 `POST /v1/notifications/webhooks` 建应用锚定那个，id 写进 `PAYPAL_WEBHOOK_ID` 重启（见「收款（PayPal）」） |
| `migrate-go --status` 报 “N applied, 0 pending” 但仓库里有更新的迁移 | 服务器上那份 `migrate-go` 是**旧构建**：迁移 SQL 内嵌在二进制里，不是读目录 | 先把本次构建的 `migrate-go` `install` 上去再用它判断；核库用 `select max(version) from schema_migrations`（2026-10-06：068/069 实际早已应用） |

## 详细参考（按需阅读）

- `references/deploy-commands.md` — 完整命令手册: 交叉编译/standalone 打包/上传/迁移/发布/回滚/首次搭建/nginx 全文/**Vertex 切流 + 发布门禁 (§10)**
- `references/cloudflare.md` — cs 子域名接入、SSL Full 缘由、521 排查、CF API 操作
- `references/troubleshooting.md` — 踩坑实录 (askpass/迁移 env 陷阱/Flexible 重定向环/备份目录权限等)
- `references/dev-guide.md` — **开发迭代指南**: 迁移机制 (NNN_name.sql 递增, 当前见仓库)、环境变量全表、API 面、渠道 webhook、与 WMS 同机共存、回滚点管理

## 交付后提醒用户

1. SSH 密码在本对话出现过明文, 建议尽快更换并改用密钥登录
2. `.env-go` 里 `JWT_SECRET` / `PLATFORM_CREDENTIAL_KEY` **不可随意轮换**: 前者杀光在线会话, 后者使已保存的渠道凭据无法解密
3. 新库首次启动必须设 `INITIAL_ADMIN_PASSWORD` (≥12 位), 001 迁移自带的默认 admin bcrypt 哈希是不安全占位
4. **备份目录与配置副本的清理纪律**（两件事不一样，分开处理）：
   - `/opt/khmer-ai-cs/frontend-backup-*` 只留最近 3 份；不在该命名规范里的旧树（`frontend-old` /
     `frontend.old` / `frontend.bak-*`）先 `grep -rlo <目录名> /etc/nginx/ /etc/systemd/system/` 确认无人引用再删。
   - `.env-go.bak-*` 不要直接 `rm`：每份里既有**明文密钥**（副本越多越脏）也可能躺着**唯一的回滚值**。
     做法：留最近 3 份在位，其余 `tar -czf /root/archive/env-go-backups-<date>.tgz`（`chmod 600`）后删原件。
     2026-09-27 实例：studio 中继地址（§10.5 的降落伞）在现行 `.env-go` 里已被清空，**只剩在归档里** —— 直接 `rm` 就把它删没了。
5. Postgres 备份: `pg_dump -d khmer_ai_cs` 定时跑 (pgvector 索引大, dump 会慢)
6. `server-go` 监听 0.0.0.0:8081 公网可直连 (未走 nginx 的路径也暴露), 建议防火墙限 127.0.0.1 或加 nft 规则
