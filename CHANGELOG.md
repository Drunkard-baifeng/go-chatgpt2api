# 更新记录

Status: current

## v0.1.0 - 2026-11-13

初始版本。

- [新增] 提供文本对话、图片生成与参考图编辑，以及账号、代理、用户密钥、图库、日志和实时监控。
- [新增] 控制台资源内嵌于可执行文件，网页与 API 共用端口；图片放大使用纯 Go 实现。
- [新增] 使用 `.env` 或进程环境配置，启动时自动检测并初始化 PostgreSQL；数据库独立部署。
- [新增] 图片链接自动识别访问地址和可信反向代理，无需填写公开域名；README 提供带注释的 Nginx 配置及超时说明。
- [新增] 正式可执行程序支持在线检查、下载校验、安装重启和启动失败恢复，保留配置及业务数据；容器通过拉取镜像更新。
- [新增] 提供 Windows、Linux 的 amd64、arm64 下载包和 SHA-256 校验，以及 Linux 双架构 Docker 镜像。

交流 QQ 群：1057583658。生图 API：[jisuai.net](https://jisuai.net)。部署方式和使用范围见 [使用说明](https://github.com/Drunkard-baifeng/go-chatgpt2api#readme)。
