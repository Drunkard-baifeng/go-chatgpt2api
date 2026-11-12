# go-chatgpt2api

Status: current

Go 服务内嵌 Vue 3 控制台，一个可执行文件同时提供网页与 API。支持文本对话、图片生成与编辑、账号、代理、日志和实时监控。使用 Gin、PostgreSQL，图片放大使用纯 Go `imaging.Lanczos`。

| 目录 | 说明 |
| --- | --- |
| [go_backend](go_backend/README.md) | 后端源码、配置示例、数据库迁移及接口说明 |
| [new_frontend](new_frontend/README.md) | 控制台源码、依赖锁文件和静态资源 |
| [deploy](deploy/nginx-server.conf.example) | Nginx 反向代理示例 |

## 运行环境

- Go：最低 **1.27.4**，以 [go.mod](go_backend/go.mod) 为准。
- Node.js：**22.23.5 LTS**，仅用于前端安装与构建，以 [版本文件](new_frontend/.node-version) 为准。
- PostgreSQL：使用独立数据库；[Compose 示例](docker-compose.yml) 使用 PostgreSQL 18。

安全补丁记录见 [Go 官方记录](https://go.dev/doc/devel/release#go1.27.4) 和 [Node.js 官方说明](https://nodejs.org/en/blog/release/v22.23.5)。部署只需可执行文件、配置、PostgreSQL 和数据目录，无需 Node.js 或静态服务器。升级 Go 后须重新编译并重启服务。

<a id="构建与部署"></a>

## 构建与启动

在仓库根目录执行：

```powershell
npm --prefix new_frontend ci --registry=https://registry.npmjs.org
node scripts/build.mjs
if (!(Test-Path ./go_backend/config.yaml)) { Copy-Item ./go_backend/config.example.yaml ./go_backend/config.yaml }
```

构建产物位于 `bin/`，已经包含全部控制台资源。填写 `go_backend/config.yaml` 的 `auth.admin_key` 和 PostgreSQL 连接信息；数据库角色须事先存在，创建新数据库时需要 `CREATEDB` 权限。Windows 新空库先迁移，再启动：

```powershell
.\bin\chatgpt2api-windows-amd64.exe db migrate --config .\go_backend\config.yaml
.\bin\chatgpt2api-windows-amd64.exe serve --config .\go_backend\config.yaml
```

打开 `http://127.0.0.1:8100`，使用管理员密钥登录；API 也使用此端口，就绪检查为 `/health/ready`。部署时可将可执行文件和配置复制到任意目录，用 `serve --config ./config.yaml` 启动。相对数据路径以配置所在目录为准；已有配置和数据请保留。

交叉编译可设置 `GOOS`、`GOARCH` 后重跑构建，例如 Linux amd64：

```powershell
$env:GOOS = 'linux'
$env:GOARCH = 'amd64'
node scripts/build.mjs
```

发布程序时一并保留仓库及第三方许可文件，见文末链接和 [编码资源说明](go_backend/internal/images/codec/README.md)。

## 本地开发

另开终端，在仓库根目录执行：

```powershell
npm --prefix new_frontend run dev
```

打开 `http://127.0.0.1:5176`，开发代理默认连接 8100。前端改动通过 Vite 热更新；重新打包后才会更新可执行文件中的页面。只准备内嵌资源可执行 `node scripts/build.mjs --prepare`，之后可在后端目录使用 `go run`、`go test` 或 `go build`。干净检出缺少内嵌资源时，直接执行 Go 编译会失败。

## 容器部署

见 [Compose 示例](docker-compose.yml)：复制 `.env.example` 为 `.env` 并填写数据库密码，配置中使用 `server.host: 0.0.0.0`、`database.host: postgres`，凭据与 `.env` 一致。构建上下文为当前目录，镜像自动构建并嵌入控制台：

```powershell
docker compose build api
docker compose up -d postgres
docker compose run --rm api db migrate --config /app/config.yaml
docker compose up -d api
```

直接访问 8100 即可。需要 HTTPS 时可加 [Nginx 全站反代](deploy/nginx-server.conf.example)，无需上传静态目录。当前只支持单活动后端进程，备份须同时保留数据库和图片、任务、文件目录；使用数据库备份功能时还需可用的 `pg_dump`，镜像已包含客户端。

接口参数、限制及已验证范围见 [后端说明](go_backend/README.md)。许可见 [LICENSE](LICENSE)、[NOTICE](NOTICE) 和 [前端第三方声明](new_frontend/THIRD_PARTY_NOTICES.md)。

## 来源与许可

基于 yukkcat/chatgpt2api 的业务行为重构，保留来源及第三方许可。源码许可见 [LICENSE](LICENSE)，版权与上游声明见 [NOTICE](NOTICE)，控制台参考来源见 [第三方声明](new_frontend/THIRD_PARTY_NOTICES.md)。
