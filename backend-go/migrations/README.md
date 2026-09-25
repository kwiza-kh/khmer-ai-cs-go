# 此目录不生效 —— 真正的迁移在 `internal/migrations/migrations/`

**不要在这里新增迁移并期望它被执行。** 迁移运行器（`cmd/migrate`）嵌入的是：

```
internal/migrations/migrations.go:  //go:embed migrations/*.sql
```

即 `internal/migrations/migrations/`。本目录只是**供 psql 手查的镜像副本**，Go 代码不读它，
`.sql` 文件放在这里永远不会被执行，也**不会有任何报错**。

## 新增迁移的正确流程

1. 在 `internal/migrations/migrations/` 建 `0NN_名字.sql`（三位数递增）。
2. **把同一个文件逐字节复制到本目录**（`cp`，不要手写第二份）。
3. 跑 `go test ./internal/migrations/` 对账，再跑 `go test ./...`。

第 2 步不是可选的：本目录与嵌入集合由 `TestMirrorDirectoryIsInSync`
（`internal/migrations/migrations_test.go`）**逐字节**比对，数量或内容不一致即测试失败。
这条路是踩过坑才加的——镜像曾悄悄落后 13 个文件（停在 036，嵌入集合已到 049），
照着镜像查 schema 的人看到的是错的 schema，比没有更糟。

## 为什么不删掉这个目录

它看起来像"死副本"，但删除会同时破坏两件事，所以保留：

* `TestMirrorDirectoryIsInSync` 会因为镜像目录缺失（`os.ReadDir` 报错）而 `t.Skipf`
  —— 对账保护静默失效，将来顶层再多一份"看起来更显眼"的目录也没人拦住；
* `deploy-khmer-ai-cs/references/dev-guide.md` §2 与 `docs/DEVELOPMENT.md` 都把这个
  目录写成"psql 手查用"的正式交付物，部署/排障文档指向它。

维护规则就一条：**改嵌入集合，就 `cp` 一份过来。**

## 为什么这个 README 放在这里

因为"无声不生效"正是这个目录唯一真实的风险：文件名看起来和真源一样，
放错了地方却只在生产里表现为"迁移没跑"。README 的作用是让下一个打开
这个目录的人在两秒内看到这句话，而不是去查 `//go:embed` 指向哪里。
