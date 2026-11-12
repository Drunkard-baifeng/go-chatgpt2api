# go-chatgpt2api

Status: current

独立的 Go 后端与 Vue 3 控制台，提供文本对话、图片生成与编辑、账号管理、代理、日志和实时监控。后端使用 Gin、PostgreSQL，图片放大使用纯 Go `imaging.Lanczos`。

| 目录 | 说明 |
| --- | --- |
| [go_backend](go_backend/README.md) | 后端源码、配置示例、数据库迁移及接口说明 |
| [new_frontend](new_frontend/README.md) | 控制台源码、依赖锁文件和静态资源 |
| [deploy](deploy/nginx-server.conf.example) | Nginx 反向代理示例 |

## 运行环境

- Go：最低 **1.27.4**，以 [go.mod](go_backend/go.mod) 为准。
- Node.js：**22.23.5 LTS**，仅用于前端安装与构建，以 [版本文件](new_frontend/.node-version) 为准。
- PostgreSQL：使用独立数据库；[Compose 示例](docker-compose.yml) 使用 PostgreSQL 18。

这两个运行时补丁包含安全修复，来源见 [Go 官方记录](https://go.dev/doc/devel/release#go1.27.4) 和 [Node.js 官方说明](https://nodejs.org/en/blog/release/v22.23.5)。升级 Go 后须重新编译并重启服务。后端运行不需要 Python 或 Node.js。

## 启动后端

在仓库根目录执行：

```powershell
cd go_backend
if (!(Test-Path ./config.yaml)) { Copy-Item ./config.example.yaml ./config.yaml }
```

填写 `config.yaml` 的 `auth.admin_key` 和 PostgreSQL 连接信息。数据库角色须事先存在；创建新数据库时需要 `CREATEDB` 权限。新空库先迁移，再启动：

```powershell
go run ./cmd/chatgpt2api db migrate --config ./config.yaml
go run ./cmd/chatgpt2api db verify --config ./config.yaml
go run ./cmd/chatgpt2api serve --config ./config.yaml
```

后端默认地址为 `http://127.0.0.1:8100`，就绪检查为 `/health/ready`。已有配置不要覆盖；数据库和文件目录不要直接共用 Python 的运行数据。

## 启动前端

另开终端，在仓库根目录执行：

```powershell
npm --prefix new_frontend ci --registry=https://registry.npmjs.org
$env:VITE_DEV_API_TARGET = 'http://127.0.0.1:8100'
npm --prefix new_frontend run dev
```

打开 `http://127.0.0.1:5176`，使用配置中的管理员密钥登录。开发代理未设置上述环境变量时默认连接 8099，因此连接 Go 时需要显式指定 8100。

## 构建与部署

在后端目录执行 `go build -o ./bin/chatgpt2api ./cmd/chatgpt2api`；在前端目录执行 `npm run build`，产物位于 `new_frontend/dist`。

前端静态文件由 Nginx 托管，接口转发到 Go，参考 [Nginx 示例](deploy/nginx-server.conf.example)。容器配置见 [Compose 示例](docker-compose.yml)：容器内配置使用 `server.host: 0.0.0.0`、`database.host: postgres`，数据库凭据与 `.env` 保持一致，新空库需先执行迁移。当前只支持单活动后端进程，备份须同时保留数据库和图片、任务、文件目录。

接口参数、限制及已验证范围见 [后端说明](go_backend/README.md)。许可见 [LICENSE](LICENSE)、[NOTICE](NOTICE) 和 [前端第三方声明](new_frontend/THIRD_PARTY_NOTICES.md)。

## 来源与许可

基于 yukkcat/chatgpt2api 的业务行为重构，保留来源及第三方许可。源码许可见 [LICENSE](LICENSE)，版权与上游声明见 [NOTICE](NOTICE)，控制台参考来源见 [第三方声明](new_frontend/THIRD_PARTY_NOTICES.md)。
