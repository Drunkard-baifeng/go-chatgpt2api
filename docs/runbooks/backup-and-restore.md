# Go 备份与恢复

Status: current

本手册说明 Go 备份格式和离线恢复步骤。Go 自有数据的真实 PostgreSQL 导出/恢复、三域对账及重启切换已有隔离证据；具体范围见 [后端现状与限制](../../go_backend/README.md)。Python 业务文件格式、生产及容器恢复仍有独立门槛，见 [部署与切换](deployment-and-cutover.md)。不能把下列命令当作已执行记录。

## 备份入口与依赖

在控制台“系统设置 → R2 备份”填写账号、桶、前缀和权限，先测试连接，再手动备份。API 为管理员 `POST /api/backup/test`、`POST /api/backups/run`、`GET /api/backups`、`POST /api/backups/delete`。敏感配置经设置接口保存，不在 URL 或命令参数中粘贴令牌。列表、轮换和删除只作用于受管前缀下符合命名规则的备份对象。

运行环境须有 `pg_dump` 17+，主版本不能低于 PostgreSQL 服务端。仓库 Dockerfile 安装 18 客户端，当前 Compose 数据库也是 18；自建环境通过 `pg_dump --version` 核对。备份使用 YAML 中的显式数据库参数，不继承 `PG*`、密码文件或用户证书。数据库 `verify-ca` 在 libpq 的系统 CA 模式下收紧为 `verify-full`，因此还要求主机名匹配；真实 TLS 备份连接尚未验证。

归档最多 512 MiB、100000 项；R2 列表分页同样有界，超限明确失败。缺少任一启用领域的快照、数据库导出失败或网络失败时，不上传伪完整结果。正常后台备份允许继续运行请求，各域分别读取稳定快照，数据库与文件之间没有全局一致时点。需要用于服务切换的静止快照时，先停止全部写入者，再按下节手工导出；当前没有生产离线归档 CLI，不能在服务停止后调用 HTTP 备份接口。

归档内容：

| 路径 | 含义与所有者 |
| --- | --- |
| `backup-metadata.json` | 格式版本、生成时间、触发方式、应用版本和数据库类型 |
| `data/application-database.pgdump` | `pg_dump --format=custom --no-owner --no-privileges`，包含全部既有领域数据和迁移台账 |
| `data/images/` | 可选；图片服务输出索引、原图及受管缓存，WebDAV 原图通过服务入口读取 |
| `data/image_tasks/` | 可选；图片任务索引与仍需保留的私有活动输入 |
| `data/files/` | 可选；成功 PPT/PSD 任务的产物及安全元数据快照；任务权威记录仍在数据库 |

YAML 启动文件不包含在归档中。恢复时单独准备与目标环境匹配的配置和密钥。数据库、文件和活动输入可能包含敏感内容，解密和解压目录只授予操作者访问权限。

## Go 停机快照

此流程面向 Go 已有数据，输出一个维护快照目录，与 R2 服务归档分开。内部归档测试调用领域快照入口，不能视为已有可执行的离线产品命令；下列手工流程才是运维入口。Python 的文件格式仍须先满足 [接管门槛](deployment-and-cutover.md#python-数据接管门槛)。

停止入口写入、应用进程、任务和维护脚本后，确认数据库及三个目录没有其他写入者。源库和原目录保持不动，用新的空目录执行以下操作；示例为 Linux 裸机，替换显式库名、角色及源路径。只在每一步成功后继续下一步：

```sh
mkdir -m 700 snapshot
mkdir -m 700 snapshot/data
pg_dump --host=127.0.0.1 --port=5432 --username=snapshot_operator --dbname=chatgpt2api_go --password --format=custom --no-owner --no-privileges --file=snapshot/data/application-database.pgdump
cp -a -- /srv/chatgpt2api/data/images snapshot/data/images
cp -a -- /srv/chatgpt2api/data/image_tasks snapshot/data/image_tasks
cp -a -- /srv/chatgpt2api/data/files snapshot/data/files
pg_restore --list snapshot/data/application-database.pgdump
```

数据库密码交互输入，连接参数和客户端 TLS 环境须对应选定源库。复制须包括隐藏索引、标签、任务文件及产物；不要仅复制图片或公开文件。停机后复制的 `writer.lock` 文件不是活动锁，不能凭文件存在判断进程是否运行。YAML 单独保管，不放进快照。确认导出退出码、三目录复制结果及文件摘要，保留快照来源、程序构建和停止时点记录；不要在源服务仍运行时用普通复制替代领域快照。

容器场景先停 API，保持 PostgreSQL 可连接，再使用匹配主版本的客户端导出，并通过只挂载该数据卷的维护环境复制三目录；不要启动应用去执行导出。当前没有容器演练证据，首次容器迁移须先在副本跑通卷导出及恢复。

此快照没有 `backup-metadata.json`，也没有 R2 归档的加密和大小边界；不能伪装成服务归档或交给在线接口恢复。恢复时直接使用该目录中的 `data/application-database.pgdump` 与三个完整目录，按下文“在独立副本演练恢复”执行，并逐行及逐文件对账。

## 下载、解密与检查

通过受信的 R2 管理工具下载选定备份，不修改服务中的对象。将其放在新建隔离目录，保留原始对象及可信校验值。检查大小、对象来源、应用版本和包含项；AES-CBC 格式沿用既有 OpenSSL 兼容行为，本身不提供认证标签，不能靠“可解密”证明来源完整性。

未加密文件直接进入清单检查。加密文件以 `Salted__` 开头，使用 AES-256-CBC、PBKDF2-HMAC-SHA256、10000 次、8 字节盐及 PKCS#7；使用支持 `-saltlen` 的 OpenSSL：

```sh
openssl enc -d -aes-256-cbc -pbkdf2 -iter 10000 -md sha256 -saltlen 8 -in backup.tar.gz.enc -out backup.tar.gz
tar -tzf backup.tar.gz
```

OpenSSL 交互输入口令，不写入命令历史。先检查清单没有绝对路径、`..`、符号链接或不认识的领域，再解压到新的空目录。解密失败、清单缺失、大小不符或格式版本不认识时停止恢复，不覆盖现用目录。

```sh
mkdir restore-staging
tar -xzf backup.tar.gz -C restore-staging
pg_restore --list restore-staging/data/application-database.pgdump
```

## 在独立副本演练恢复

1. 停止该副本的所有 Python、Go、维护脚本和后台写入者；生产切换必须另有明确授权。保存目标当前快照及 YAML，不在运行中的数据库或文件目录上覆盖。
2. 由数据库管理员创建新的空数据库及应用角色，将该角色设为目标库所有者，配置连接仅指向该隔离目标。以下恢复使用与目标 YAML 相同的应用角色，`--no-owner` 使恢复对象归属于执行恢复的角色；不要以其他管理员身份恢复后遗漏应用权限。使用不低于备份服务端主版本的 `pg_restore`，交互输入密码：

   ```sh
   pg_restore --host=127.0.0.1 --port=5432 --username=chatgpt2api --dbname=chatgpt2api_restore --password --no-owner --no-privileges --exit-on-error --single-transaction restore-staging/data/application-database.pgdump
   ```

3. 将已包含的三个文件领域分别恢复到目标 YAML 的 `images.directory`、`image_tasks.directory` 和 `editable_files.directory`。三个目录互不相同、不得嵌套，且只有一个活动进程可用；恢复完成前保持服务停止。归档文件权限为 `0600`，容器运行用户为 `10001:10001`，须对本次新恢复的三个目录及其文件设置相应归属和读写权限；只调整这三个已确认的目标，不修改其他数据卷。不要把 `data/files/editable-file-tasks.json` 当成独立数据库导入工具，也不要用缺少文件领域的备份覆盖对应现有资产。
4. WebDAV 索引保留远端位置，包含本地字节不代表自动完成远端迁移。保持原受管远端可读，或在独立副本中按资产所有者验证复制与索引协调；当前没有任意远端位置的自动恢复命令，不能直接批量改写索引。
5. 使用目标 YAML 执行只读 `db status` 和 `db verify`，不要先执行基线迁移。Go 归档应包含迁移台账；需要接管 Python 数据库时遵守独立的离线接管流程，不把缺失台账静默忽略：

   ```sh
   chatgpt2api db status --config ./restore.yaml
   chatgpt2api db verify --config ./restore.yaml
   ```

6. 验证账号与密钥数量、设置、代理、日志和提示词；启动单个隔离服务，检查健康、图库原图/缩略图、图片任务及 PPT/PSD 下载。遗留活动任务会中断，不自动重放；分别核对任务元数据与文件资产。验证自动清理和 R2 凭据仅指向测试资源，避免启动副本误操作现用集成。

上述 Go 归档恢复不转换 Python 的旧图库、图片任务或文件记录。完整数据范围、迁移阻塞和无双写切换顺序见 [部署与切换](deployment-and-cutover.md)。本阶段不提供自动恢复或破坏性降级命令。
