# Go 后端图谱

Status: current

本图谱描述当前业务模块、所有权及关键调用链。文本与思考规则沿用 Python。完整输入子集、迁移阻塞、外部验证和用户验收状态见 [后端运行说明](../../go_backend/README.md)；部署入口见 [部署与切换](../runbooks/deployment-and-cutover.md)。

| 所有者 | 当前职责与入口 |
| --- | --- |
| [应用入口](../../go_backend/cmd/chatgpt2api/main.go) | 解析显式命令及配置路径，组装依赖，创建监听，管理启动校验、请求排空及资源关闭 |
| [启动配置](../../go_backend/internal/config/config.go) | 严格解析单个 YAML，提供有界连接、请求和关闭策略，不接受环境覆盖 |
| [数据库](../../go_backend/internal/database/database.go) | pgx stdlib 唯一业务连接池，Ent 包装同一池，确认缺失后创建数据库 |
| [结构生命周期](../../go_backend/internal/database/migrations.go) | 只读状态与结构指纹、显式 goose 基线、旧结构离线接管、锁与版本拒绝 |
| [身份服务](../../go_backend/internal/auth/service.go) | Bearer 管理员与用户密钥识别，身份及能力投影；同包 [PostgreSQL 适配器](../../go_backend/internal/auth/postgres.go)逐次读取身份，成功后记录最近使用时间并按密钥每 60 秒限频持久化，不以时间缓存替代身份查询 |
| [HTTP 适配](../../go_backend/internal/app/router.go) | Gin 路由、访问边界、显式跨域来源、协议错误、健康检查、请求 ID、脱敏日志和异常恢复 |
| [文本 HTTP 适配](../../go_backend/internal/app/text.go) | 目录及聊天路由；文本序列化；同文件响应交付器供文本和图片共用，反馈真实 Write/Flush 错误 |
| [图片 HTTP 适配](../../go_backend/internal/app/images.go) | 鉴权后由执行者准入，再读取有界 JSON/multipart；Python 同形 JSON、参数错误和图片 SSE、当前来源绝对 URL、公开随机资产读取及写入截止 |
| [图片任务 HTTP 适配](../../go_backend/internal/app/image_tasks.go) | 有效身份提交、归属列表和只读续查；输入截止、大小限制及任务错误形状 |
| [图库 HTTP 适配](../../go_backend/internal/app/gallery.go) | 管理员图库查询与变更、ZIP/单图下载、清理；公开缩略图及写入截止 |
| [兼容协议 HTTP 适配](../../go_backend/internal/app/protocols.go) | Responses/Messages/搜索的有界读取、SSE事件与响应序列化；共用文本/图片执行者 |
| [协议请求适配](../../go_backend/internal/chat/protocol_request.go) | 将支持的文本历史、思考和工具结构映射到既有请求，不启动生成生命周期 |
| [可编辑文件任务](../../go_backend/internal/editablefiles/service.go) | 独立领域 PostgreSQL 元数据、有界输入和队列、owner幂等、PPT/PSD校验、文件发布与墓碑、重启清理；复用既有账号 Lease 和调用记录 |
| [观测领域](../../go_backend/internal/observability/service.go) | 订阅调用开始/进度/结束，统一投影日志与实时详情的安全参数、逐图尝试、阶段事件及耗时分析；保留有界脱敏上游错误、文本和原始诊断，区分 API 与上游状态并明确历史缺失。总览使用生命周期墙钟，细分耗时避免重复相加。[实时汇总](../../go_backend/internal/observability/monitor_summary.go)从完成窗口输出六组指标，接入执行者实际容量、图片出口租约及有界入口拒绝采样，不增加执行队列。同包 metrics 使用既有指标表，[运行环境采样](../../go_backend/internal/observability/runtime.go)独占一秒缓存及 CPU/网络差分基线，采集进程、可见系统资源和配置图片目录所在磁盘 |
| [提示词领域](../../go_backend/internal/prompts/service.go) | 内嵌独立快照、来源启停、有界远程更新及失败时保留旧快照，使用既有领域表 |
| [远程导入领域](../../go_backend/internal/remoteimports/service.go) | CPA/sub2api来源、列表和任务进度、重启中断；同包OAuth持有state/PKCE短期会话，账号写入仍走Account Repository |
| [备份领域](../../go_backend/internal/backup/service.go) | R2签名操作、pg_dump、各所有者快照、归档/加密、调度与轮换；不拥有被备份领域的写入 |
| [清理编排](../../go_backend/internal/cleanup/service.go) | 手动预览/执行与30分钟定时检查，委托日志、图片、账号所有者，不重新维护资产或账号状态 |
| [升级能力](../../go_backend/internal/updates/service.go) | 如实投影当前Go无在线安装包，保留部署工具升级入口说明 |
| [受管集成传输](../../go_backend/internal/managedhttp/client.go) | CPA/sub2api、WebDAV和GenBox共用的固定来源、DNS拨号校验、禁跳转、显式预算和取消；允许管理员配置的有效私网端点 |
| [账号仓储](../../go_backend/internal/accounts/repository.go) | 批量导入与完整 AT 去重、稳定管理 ID、事务修订和 CAS；同包管理操作、分组与安全投影 |
| [账号维护](../../go_backend/internal/accounts/maintenance.go) | 有界同步与刷新、后台任务进度、取消收口、旧凭据 CAS；[后台生命周期](../../go_backend/internal/accounts/lifecycle.go)启动即扫描待核验登录态、临期 AT 和限流账号，按当前维护间隔调度，与手动维护共用容量并避开占用账号；Remote Adapter 负责上游网络 |
| [账号池](../../go_backend/internal/accounts/pool.go) | 可用性选择、轮转、续期后当前账号及分组准入复查、账号图片容量及 Lease 释放、调用计数；[图片选号预检](../../go_backend/internal/accounts/image_preflight.go)拥有来源与套餐归一化、模型条件过滤、最多 20 个不同候选的远程核验及资格复查，沿用已选出口，落选不计生成失败；编排代理容量，不拥有第二套代理计数 |
| [设置领域](../../go_backend/internal/settings/service.go) | 单一字段规范、脱敏投影、修订冲突、敏感项清空及执行用配置；[控制台日志处理器](../../go_backend/internal/settings/console.go)按当前 `log_levels` 筛选进程输出，调用记录独立保存 |
| [代理领域](../../go_backend/internal/proxy/service.go) | 独立配置、引用与删除保护、出口选择、节点容量及固定目标连接探测；探测投影出口 IP、地区、HTTP 和时间，组摘要保留部分成功，最近健康仅在进程内有界保存 |
| [用户密钥管理](../../go_backend/internal/auth/keys.go) | 普通用户密钥 CRUD、随机生成及摘要保存，复用即时身份查询与最新使用时间投影；管理写入与使用时间写入共享集合修订锁，删除后清理进程内时间 |
| [控制面 HTTP 适配](../../go_backend/internal/app/accounts.go) | 管理账号与分组；同目录 control/keys/account_probe 适配设置、代理、密钥及复用生成执行者的账号测试 |
| [文本请求与目录](../../go_backend/internal/chat/request.go) | 文本输入契约、模型透传及 Python 思考优先级与归一化；合并图片模块定义、本地可用 Codex 套餐及当前运行时能力，输出 GIF/SVG 共用模型列表与随能力、账号套餐变化的目录修订号；[目录 HTTP 适配](../../go_backend/internal/app/text.go)统一两个目录入口，不触发远程核验 |
| [文本执行者](../../go_backend/internal/chat/service.go) | 唯一调用生命周期，有界活动槽位、总截止、初始记录、账号选择、独立会话、最终交付及终态清理；不自动换号或重放 |
| [调用记录仓储](../../go_backend/internal/callrecord/repository.go) | 既有调用表中的初始记录和一次终态更新；同包 Trace 只持有有界、并发隔离的结构化诊断快照，写入既有 payload，不拥有执行生命周期；保存有界输入文字及最多 4000 字符的脱敏失败摘录，不保存完整上游响应、原图或凭据 |
| [图片输入](../../go_backend/internal/images/request.go) | Python 参数默认值、空值、额外字段及 schema 错误，JSON/multipart、base64 和参考图/遮罩共享预算；[遮罩合成](../../go_backend/internal/images/mask.go)复用图片解码锁；归一化 GIF/PNG/JPEG/WebP/SVG 输出格式，统一 GIF/SVG 模型、流式及 SVG 无参考图约束 |
| [图片执行者](../../go_backend/internal/images/service.go) | 同步与异步共用有界批次、总截止、调用记录及发布生命周期；[逐图执行](../../go_backend/internal/images/execution.go)拥有每图 Lease／会话、可配置并行、有限不同账号换号、多槽检查点及只读恢复，不重放受理状态不确定的提交；消费二次确认配置，SVG 使用 `auto` 文本链提取安全 XML，GIF/SVG 跳过超分，资产保存并被任务接受后按设置隐藏图片会话 |
| [图片任务服务](../../go_backend/internal/imagetasks/service.go) | 有界队列、身份与客户端 ID 幂等、归属、任务及私有恢复证据、活动输入临时文件、重启中断、保留期和关闭收口；不拥有图片写入 |
| [图片存储服务](../../go_backend/internal/images/assets.go) | 独立资产目录与catalog/gallery/thumbnail/WebDAV/GenBox的唯一所有者；本地与远端原图、标签、代次、墓碑、启动协调、压缩、下载、快照和清理；[公开地址解析](../../go_backend/internal/images/public_url.go)统一 YAML、业务设置和请求来源优先级；[发布前图片处理](../../go_backend/internal/images/output.go)在同一解码锁内完成一次解码、EXIF 方向修正、[按需放大](../../go_backend/internal/images/output_upscale.go)和最终编码，无需处理时保留原字节，放大统一使用纯 Go `imaging.Lanczos`；GIF 保留动画原字节并限制帧预算，[静态 SVG 验证](../../go_backend/internal/images/output_svg.go)保护保存与读取，矢量缩略图返回安全原文；[WebP 编码](../../go_backend/internal/images/output_webp.go)使用有界、可取消的 WASM 实例，自动推送队列同生命周期关闭 |
| [用量估算](../../go_backend/internal/usage/usage.go) | 内嵌词表文本计数及 Python 图片 patch 估算；统一图片、Chat Completions、Responses 和 Messages 的用量投影，不表示上游计费统计 |
| [目录锁](../../go_backend/internal/filelock/lock.go) | 为任务和图库各自提供操作系统文件锁，不拥有业务状态 |
| [外部资产传输](../../go_backend/internal/assettransfer/transfer.go) | 无账号认证的 GET/PUT，逐跳公网校验并固定实际 IP，保留 Host/SNI、有界重定向和数据；不读环境代理。PUT 对齐 Python 上传头及 120 秒预算，GET 为 60 秒，均服从父截止；直连仅在 TCP/TLS 建立前尝试其他已验证地址，不重放 PUT；结构化返回安全传输阶段、实际 HTTP 状态及有界失败正文，由上游适配器脱敏 |
| [上游会话](../../go_backend/internal/upstream/session.go) | 单个账号及代理组合的 Cookie、连接、固定目标、传输截止、取消和有界响应体，由对应执行者创建并关闭；请求语言头对齐 Python 中文默认值，具体值见后端说明的 TLS 会话章节；同包 diagnostics 采集安全 HTTP/SSE 元信息和协议阶段，缺失的传输回调明确标记；失败正文通过有界脱敏诊断传递，不记录凭据、请求头或签名地址 |
| [共享准备与文本协议](../../go_backend/internal/upstream/protocol.go) | 文本/图片共用首页和 Sentinel prepare/finalize；单次文本提交；同包 PoW、受限挑战虚拟机及 SSE 正文解析 |
| [上游账号协议](../../go_backend/internal/upstream/account.go) | 只读账号资料/额度查询；隔离 OAuth 刷新，不提交生成，不携带 ChatGPT Cookie 到授权站点 |
| [上游图片协议](../../go_backend/internal/upstream/image.go) | 按 Python 顺序先登记、上传并确认参考图，再准备首页、Sentinel、conduit 并单次提交；统一附件名及多图引用顺序。SSE 编码元数据与对象合并 patch、已知会话只读恢复；兼容省略操作的消息元数据更新，普通升级提示不阻断图片；识别面向用户的 commentary 终态文字，保留上游原文并停止空轮询，区分轮询阶段预算与总截止，附带有界脱敏的最近消息诊断；按配置确认图片引用稳定后解析和下载；提取任务、会话、HTTP/SSE 失败原因及文本结果，不拥有公开调用终态 |
| [上游 Codex 图片协议](../../go_backend/internal/upstream/codex_image.go) | 单次 Responses 生图提交、data URL 参考图、受限 JSON/SSE 解析和完整图片校验；不走网页准备、上传及会话续查，取首张有效结果，保留有图后附带失败供凭据生命周期消费，不重放提交状态未知的请求 |
| [上游搜索协议](../../go_backend/internal/upstream/search.go) | 固定网页搜索模型、单次提交、SSE结束/error和已知会话只读查询 |
| [上游文件协议](../../go_backend/internal/upstream/editable.go) | 固定思考模型、参考图上传、单次提交、当前分支完成判定、主文件与ZIP同集合选择及下载，不拥有任务终态 |

入口加载 YAML、离线检查 TLS、打开并验证 PostgreSQL，组装各独立领域，安装运行时日志筛选、工作者及后台账号维护调度器，再创建 Gin 开始监听。关闭时先排空HTTP，再停止清理、备份、文件和图片任务，等待工作实际退出后关闭图片资产及其自动推送、提示词、远程导入、账号与后台维护及数据库。任务有界关闭超时不会提前释放仍被使用的依赖。`db migrate`、`db adopt` 使用独立命令路径；普通启动不建表、不登记迁移台账。

Ent 映射 `accounts` 和 `auth_keys`，其余领域通过同一SQL池在既有表操作各自数据；仍为17张领域表，没有新增通用仓储或第二连接池。短事务保护领域变更，网络操作不跨期持有数据库连接。运行时不使用Ent自动建表。迁移SQL是结构变更的唯一来源；结构检查排除对象所有者、OID和序列当前值，但单独校验自增ID位置。

已实现的HTTP路由、输入限制及命令以 [运行说明](../../go_backend/README.md) 为准。原延期路由均已移入对应处理器并保留鉴权，不再保留统一501清单；关闭能力及不支持的输入仍明确返回诊断，不能用路由登记数代替流程验收。

文本调用的数据流为：HTTP 鉴权与解析 → 文本执行者保存初始调用记录 → 账号池预留账号及代理容量 → 独立上游会话准备与提交 → SSE 正文投影 → HTTP 交付回调 → 调用记录终态。HTTP 层只拥有交付，执行者拥有其预算与成功判定。响应交付与数据库终态无法原子提交，失败或进程强杀可能残留 `running`，本阶段没有重启恢复作业。

图片调用的数据流为：HTTP 鉴权 → 图片执行者准入并开始整批预算 → HTTP 读取、解析及运行时格式准入 → 初始调用记录 → 按模型来源和套餐选择候选、分别预留账号和代理容量、远程核验并复查资格 → 读取及合成参考图／遮罩 → 独立上游会话准备、上传和单次生成 → SSE／只读查询及二次确认、签名下载 → 按请求转换静态图片编码 → 按运行时开关超分 → 图片存储服务发布 → 实际尺寸及本地用量投影 → HTTP JSON/SSE 交付 → 记录终态。Codex 在上游步骤直接调用 Responses 并解析内联图片，不走网页上传和轮询；SVG 使用文本名额及 `auto` 文本会话和安全 XML 提取，不进行图片预检。GIF/SVG 保留原字节并跳过超分。`n` 张图片默认并行，可配置串行；预检落选释放容量且不消耗生成换号次数，每次生成尝试独立释放 Lease，符合条件的失败排除已尝试账号并清除旧检查点后换号，同批满载仅等待本批释放名额，所有尝试共享总截止。同步普通部分失败仍交付成功项；整批错误回收未被任务接受的资产，公开响应已交付后记录失败则保留产物并记录安全诊断。

异步图片的数据流为：HTTP 鉴权 → 任务服务预留名额并读取有界输入 → 原子保存任务及私有活动输入 → 返回任务行 → 有界工作者调用同一图片执行者 → 串行保存每张图片的账号、会话和已发布结果检查点 → 持久化终态 → 调用记录与 Lease 收口。HTTP 断连不撤销已受理任务；队列只接受执行者的结果，不另做生成或调用计数。续查只传各未完成图片已保存的原账号和会话，不重放或补发输入，兼容旧单槽恢复文件。

Python和Go运行源码相互独立，新前端通过显式HTTP目标选择服务，不自动回退。图片任务与资产仍在数据库之外，分别独占Go任务目录和图片目录；可编辑文件元数据仍在PostgreSQL，其产物使用独立目录。三个目录禁止相同/嵌套，不共享Python运行文件。图库变更统一经过图片存储服务，任务过期不代删资产；远端读取、压缩和备份都消费同一资产入口。

管理写入的数据流为：HTTP 管理员鉴权与有界输入 → 所属领域校验 → 独立领域事务/修订与引用保护 → 后端投影及事件 → 前端渲染。批量导入本地保存原子完成，之后远程维护独立执行；网络工作不持有数据库事务锁。刷新结果以旧凭据 CAS 保存，资料查询失败与凭据轮换分别处理。账号组和代理引用变更共享既有引用锁，避免并发删除被引用的组。

账号维护任务目前只存在进程内，关闭时取消，重启后不恢复；后台扫描随进程重新启动并读取当前账号状态，与文件型图片任务分别维护生命周期。图片任务重启保留结果并中断遗留工作，不自动重放；已知会话可按能力续查。备用出口及其他尚未接入的配置不代表执行能力已启用，具体生效范围以运行说明为准。
