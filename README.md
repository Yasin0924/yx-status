# YxStatus

只读服务器状态监控系统：Go Server、只读 Agent、原生 Web 看板。公开仓库仅保存源码、测试与网站静态资源；**不包含生产运行配置或可直接部署的密钥**。

## 本地构建与检查

```bash
go test ./...
go build ./cmd/server
go build ./cmd/agent
```

构建命令会在当前目录生成 `server`、`agent` 可执行文件（已被发布白名单排除）。生产配置必须在本地安全存放、权限限制为 `0600`，不能提交。Server 通过 `-config /path/to/server.json` 加载节点 ID/密钥；Agent 通过 `-config /path/to/agent.json` 加载本机节点身份与密钥。缺少节点密钥时程序拒绝启动。示例字段见 `cmd/server/main.go` 与 `cmd/agent/main.go` 的配置结构。

## 代码同步边界

本仓库仅保存允许公开的代码和静态资源。`config/`、`HANDOFF.md`、内部审查记录、运行数据、构建产物及设计过程文件不会同步。修改后需人工检查 `git status`、暂存范围、密钥扫描和测试，再显式推送；无自动从生产节点拉取或推送的同步任务。GitHub 不是运行数据备份，也不会自动部署线上服务。
