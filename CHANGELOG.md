# 更新记录

Status: current

## v0.1.0 - 2026-11-13

- [新增] 首次发布独立 Go 服务，提供文本对话、图片生成与编辑、账号、代理、用户密钥、图库、日志和实时监控。
- [新增] 控制台完整内嵌于可执行文件，网页与 API 共用监听端口，图片放大使用纯 Go 实现。
- [新增] Windows/Linux 的 amd64、arm64 下载包，包含配置示例、说明与许可文件，并提供 SHA-256 校验。
- [新增] Linux amd64/arm64 的 GHCR 容器镜像，与下载包使用同一版本标签。

下载包解压后，将 `config.example.yaml` 复制为 `config.yaml`，填写管理员密钥和 PostgreSQL 连接信息。新空库先执行 `chatgpt2api db migrate --config ./config.yaml`，再执行 `chatgpt2api serve --config ./config.yaml`；Windows 使用 `chatgpt2api.exe`。控制台和 API 默认位于 `http://127.0.0.1:8100`。

当前只支持单活动服务进程。真实外部集成和生产切换的验证范围见后端说明；本次发布不提供在线自动升级。
