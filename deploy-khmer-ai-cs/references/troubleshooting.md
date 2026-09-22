# 故障排查与踩坑记录

本部署真实踩过的坑与诊断结论。标注 [继承] 的是 WMS 同机部署时期验证过、本项目同样适用的经验。

## 1. macOS 无 sshpass → SSH_ASKPASS 机制 [继承, 已按 macOS 修正]

- `SSH_ASKPASS_REQUIRE=force` + askpass 脚本从环境变量回显密码; macOS OpenSSH 直接支持, **不需要 setsid** (Linux 才需要, 且 macOS 根本没有该命令)
- `DISPLAY=:0` 一并设置无害
- **认证被拒 ≠ askpass 没生效**: 先 `ssh -v` 确认 password 方法被尝试, 再核对密码本身
- 密码末尾是**两个点**; 口述/复制容易丢点, 连不上先核对点号数量, 不要拿错密码反复撞 (浪费会话, 若装过 fail2ban 会封禁)

## 2. SSH 会话假死 = banner exchange 超时 [继承]

- 表现为 `Connection timed out during banner exchange`, 不是密码错误
- 等 30~60 秒再试; 封禁期内反复连会延长封禁

## 3. 远程命令里 pkill 自杀（严重）[继承]

- `pkill -f "server-go"` 会匹配到含同样字样的远程 ssh 命令行自身, 把当前会话杀掉, 症状是 exit 1 无输出
- 需要清进程: `pgrep -f 模式` 拿 PID → `kill <PID>` 精确处理
- 本项目通常不需要 pkill: 一切进程操作走 `systemctl stop/start`

## 4. Text file busy [继承, 语言无关]

- 服务在跑时 cp 覆盖 `/opt/khmer-ai-cs/server-go` → `Text file busy`
- 顺序固定: `systemctl stop khmer-ai-cs-go` → cp/install → start

## 5. migrate-go 的 env 双重陷阱（本项目特有, 重要）

`migrate-go` 启动时: ① `godotenv.Load()` 只读 **`./.env`** (那是旧 Rust 后端的配置文件!), ② 需要 `DATABASE_URL`。直接 `./migrate-go` 会报 `DATABASE_URL is required` 或读到旧库配置。

正确姿势 (在 `/opt/khmer-ai-cs` 下):
```bash
set -a; . ./.env-go; set +a   # 先导出真配置
./migrate-go --status          # godotenv 不覆盖已存在变量, .env 里的旧值不会捣乱
./migrate-go
```
也可 `--dsn` 显式传库地址绕开。

## 6. Flexible SSL + 源站 301 = 重定向死循环（本项目特有）

- cs 站点 :80 块有 `return 301 https://$host$request_uri;`
- zone SSL 模式若被设为 Flexible: CF→源站走 :80 → 永远吃 301 → 浏览器无限跳转, 表现像"配置没生效"
- 与 WMS 不同 (**WMS 站点 Flexible/Full 都通, 别照搬它的经验**), 本项目 zone 必须 Full
- 同理别开 Full (strict): 源站是自签证书 → 526

## 7. 域名上没有 /health（验证方法错位）

- nginx 只把 `/api/` 转给 Go 后端; `/health` `/ready` 不在 `/api/` 前缀下
- 公网 `curl https://cs.../health` 拿到的是 Next 的 404 壳页面, **不是**后端挂了
- 后端健康 → 服务器上 `curl 127.0.0.1:8081/ready`; 公网链路 → 用 `/api/v1/auth/login` 之类的真实 API 路径验证

## 8. journalctl 里 realtime/inbox 401 刷屏 = 正常噪音

- 未登录/旧标签页的 WS 探测每 30s 重连一次, 每条 WARN 401
- 判断服务健康看 `/ready` 与登录接口, 别被这刷屏带偏

## 9. 前端接口全挂、请求打到 localhost（构建时烘焙）

- `API_BASE = process.env.NEXT_PUBLIC_API_URL || "http://localhost:8080/api/v1"` — Next 把 `NEXT_PUBLIC_*` 编译进客户端 bundle
- 发布时忘带 env 构建 = 用户浏览器请求自己的 localhost:8080, 页面能打开、接口全挂
- 修复: 带 `NEXT_PUBLIC_API_URL=https://<部署域名>/api/v1` 重新 build + 发布 (§5 流程)

## 10. standalone 缺静态资源 = 页面裸奔/白屏

- `.next/standalone` **不含** `.next/static` 和 `public/`, 单独不拷 → CSS/JS 404
- 打包必须三件套: standalone 根目录 (含 server.js + node_modules) + `.next/static` + `public` (对照 frontend/Dockerfile 第三阶段)

## 11. scp/管道退出码误报 [继承]

- `scp ... | grep -v ...` 返回的是 grep 的码; 上传成功也可能 exit 1, 以远端 `ls -lh` 为准

## 12. 服务器上 curl 自己的域名 [继承]

- 会绕 Cloudflare 一圈, 容易挂起/回环; 服务器内验证一律 `127.0.0.1 + Host 头`, 任何 curl 加 `--max-time`

## 13. 备份目录权限漂移（本项目观察到）

- `/opt/khmer-ai-cs/frontend-backup-*` 存在 777 权限的历史目录 (手工 tar/解包残留)
- 换入新前端后必须 `chown -R khmerai:khmerai frontend`, 否则 systemd 以 khmerai 起不来读不到文件

## 14. 同机 WMS 干扰 [继承]

- <部署服务器IP> 同时跑 wms.service (:3000, 主域名)。排查看 `ss -tlnp` 时端口 3000/3001 易混淆: **3001 才是本项目前端**
- 重启 nginx 会瞬断两边; 操作避开 WMS 业务高峰
- zone SSL/记录改动影响 WMS, 见 cloudflare.md

## 15. 服务器无 Go 编译器

- `command -v go` 为空 — 与 WMS 当年"服务器装 Rust 编译"策略相反, Go 一律本地交叉编译上传
- pgx/go-redis/bcrypt 均纯 Go, `CGO_ENABLED=0` 静态编译无障碍; 若未来引入需 CGO 的依赖 (如 sqlite), 交叉编译要另想办法 (zig cc / 容器构建), 先警惕

## 16. Go 监听在 0.0.0.0:8081（安全注意）

- `SERVER_PORT=8081` 绑定全网卡, **公网可直连后端**, 绕过 nginx 的安全头/日志
- 修复建议: iptables/nft 限 8081 仅本机, 或改代码绑 127.0.0.1; 在改之前, `.env-go` 泄露 = 全网可访问的严重事故
