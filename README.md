# go-chatgpt2api

Status: current

本目录收纳新版 Vue 3 控制台及独立 Go 后端。

安装前先核对 [Go 与 Node.js 运行时安全基线](docs/runbooks/deployment-and-cutover.md#运行时安全基线)；版本约束同时覆盖本地安装、CI 和 Docker 构建。

| 目录 | 状态 |
| --- | --- |
| [new_frontend](new_frontend/README.md) | 完整独立前端，包含源码、依赖锁文件、静态资源和第三方声明 |
| [go_backend](go_backend/README.md) | Gin、PostgreSQL、文本与图片协议、异步任务、账号控制面、观测及集成；实际子集与验证见后端说明 |
| [docs](docs/README.md) | 架构图谱、需求与交付记录、部署及恢复手册 |

在仓库根目录执行：

```powershell
npm --prefix new_frontend ci --registry=https://registry.npmjs.org
$env:VITE_DEV_API_TARGET = 'http://127.0.0.1:8100'
npm --prefix new_frontend run dev
npm --prefix new_frontend run build
```

构建输出为 `new_frontend/dist`。开发地址默认使用 5176 端口，预览使用 4176。
先按 [后端说明](go_backend/README.md#本地启动) 配置 PostgreSQL、执行显式迁移并启动 Go 服务。Go 默认端口为 8100；前端代理未设置环境变量时默认连接 8099，以上命令显式选择 Go。生产部署、迁移阻塞与恢复步骤见 [部署与切换](docs/runbooks/deployment-and-cutover.md)，验证范围由后端说明维护。

前端通过 HTTP 消费接口，不导入 Python 源码或原版前端文件。
本目录的 `LICENSE`、`NOTICE` 及前端第三方声明保留对应许可信息。

## 来源与许可

基于 yukkcat/chatgpt2api 的业务行为重构，保留来源及第三方许可。源码许可见 [LICENSE](LICENSE)，版权与上游声明见 [NOTICE](NOTICE)，控制台参考来源见 [第三方声明](new_frontend/THIRD_PARTY_NOTICES.md)。
