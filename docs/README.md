<a id="go-version-documentation"></a>

# Go 项目文档

Status: current

本目录维护 Go 后端与独立 Vue 控制台的当前结构、部署及恢复步骤。接口能力、参数限制和验证边界统一见后端运行说明，隔离验证不代表真实外部服务或用户验收。

- [Go 后端运行与数据库命令](../go_backend/README.md)
- [Go 后端图谱](maps/backend-map.md)
- [Go 备份与恢复手册](runbooks/backup-and-restore.md)
- [Go 部署与切换手册](runbooks/deployment-and-cutover.md)
- [Go 与 Node.js 运行时安全基线](runbooks/deployment-and-cutover.md#运行时安全基线)
- [前端页面入口、开发与部署](../new_frontend/README.md)

运行、构建和部署只依赖本仓库；不需要 Python 源码。
