# llm-gateway Release 方案

状态：待实施的设计方案。本文不代表发布工具、自更新命令或服务升级命令已经实现。

建议沿用 GoReleaser 构建，吸收 aivet 的固定资产名、单文件安装、强制校验和与显式版本更新。
GitHub Release 负责分发产物，launchctl 部署负责切换运行中的服务，两者分别验收。

## 参考与现状

参考本地 aivet 提交 `dce7a3c2555607cfc1bf6bcf39e23ce4fd1c867f`：

- `Makefile`：先 vet/test，再构建六平台裸二进制和 `SHA256SUMS`。
- `README.md`：annotated tag、推送 main/tag、构建、`gh release create` 上传。
- `install.sh` / `install.ps1`：识别平台，从 Release 下载固定名称的单文件。
- `internal/selfupdate/selfupdate.go`、`cmd/aivet/update.go`：`update --check`、指定版本、SHA-256 校验、同目录临时文件与替换。

aivet 当前安装脚本没有强制校验和，其自更新才有；本项目安装与更新都要求校验。
Windows 安装脚本直接写目标 exe 的做法也不照搬，须先下载和验证，再替换。

llm-gateway 在 `5d411f5` 的现状与建议：

| 项目 | 当前行为 | 方案 |
| --- | --- | --- |
| 构建 | GoReleaser，五个平台，排除 Windows arm64 | 六平台交叉编译验证后纳入 Windows arm64 |
| 下载资产 | 带版本名的 tar.gz / zip、checksums | 保留旧资产，增加固定名裸二进制与 `SHA256SUMS` |
| 发布触发 | push `v*` tag 后直接 release | 校验 tag、测试、构建、验收后先生成 draft |
| `make release` | 直接执行 GoReleaser 发布 | 改为只构建验证；外部发布单独命名 |
| 发布前 hooks | `go mod tidy`、`go test ./...` | 锁定依赖，vet/test；依赖漂移使构建失败 |
| 工具版本 | GoReleaser `latest` | 固定经验证版本，配置与工具一起升级 |
| 安装/更新 | 手动复制或 `make install` | 校验后的单文件安装，后续补 CLI update |
| 运行状态 | `/healthz` 返回 status、uptime，无版本 | 增加运行进程 version、commit、schema_version |
| 本地服务 | `gui/501/lol.jiangtao.llm-gateway` 指向 `/opt/homebrew/bin/llm-gateway` | 识别实际路径和 label，再部署 |

本地最近 tag 是 `v0.3.0`。此后包含 Responses API、缓存计费、用量汇总和 TypeSafe，
建议下一功能版为 `v0.4.0`，先走 `v0.4.0-rc.1`。创建 tag 前须核对远端版本占用情况。

## 资产与版本契约

GitHub 分发仓库为 `shiftu/llm-gateway`；gitea 作为代码镜像，不独立生成同版本产物。
Git tag 是版本来源，不增加需要手工同步的 VERSION 文件。
正式产物 `version` 统一输出 `llm-gateway vX.Y.Z`，候选版本保留 `-rc.N`。
当前 GoReleaser 使用 `.Version`、Makefile 使用 `git describe`，实施时统一前缀并断言产物版本。

新增分发文件：

```text
llm-gateway_darwin_arm64
llm-gateway_darwin_amd64
llm-gateway_linux_arm64
llm-gateway_linux_amd64
llm-gateway_windows_arm64.exe
llm-gateway_windows_amd64.exe
SHA256SUMS
```

裸二进制文件名不带版本；版本由 Release tag 的下载目录隔离。
现有带版本归档及 checksums 继续提供，避免破坏旧下载链接。
所有资产来自同一次 GoReleaser 构建，裸文件从该次构建结果导出，不另建一套交叉编译逻辑。
`SHA256SUMS` 覆盖所有用户下载的二进制与归档，不包含自身；旧 checksums 保持既有契约。

构建使用 `CGO_ENABLED=0`、`-trimpath`，Go 版本取自 `go.mod`。
Release 输出目录按 tag 隔离，禁止通配符混入开发构建或历史产物。
上传前将资产清单与预期矩阵逐项比对：缺失、多余、重复文件名或 checksum 失败均阻断发布。
交叉编译成功只记为编译验证，不标成已在对应系统运行验收。

## 发布流程

1. 选择干净 main 上的确定提交，核对远端 tag/Release；禁止移动已发布 tag。
2. 本地预检：vet/test、依赖无变化、GoReleaser 配置校验。
3. 在确定提交上做 snapshot 演练，检查平台矩阵、命名、版本、校验和。
4. 写人工 Release notes，覆盖版本范围内全部变化、配置差异、数据库迁移和升级限制。
5. 创建 annotated tag 并推送 origin，触发 tag 工作流。
6. 工作流验证 tag 格式、提交属于 main，再次测试、构建、校验，最后上传 GitHub draft。
7. RC 设置 prerelease，不推进 stable latest；同 tag 的发布串行化，已有资产不静默覆盖。
8. 从 draft 下载复核 checksum，至少在 macOS arm64 和 Linux amd64 跑版本、帮助与隔离实例冒烟。
9. 使用真实数据库副本验证升级，再做本机 launchctl 灰度；通过后发布 draft。

稳定版从正式 tag 重新构建并验收，不把 RC 文件简单改名。
CI 是默认发布者；本地应急发布使用同一 tag 和构建配置，先确认 CI 未同时发布。
失败 draft 查明原因后可重试，已公开版本的问题通过新版本修复。

拟定命令职责（实施后生效）：

| 命令 | 行为 |
| --- | --- |
| `make release-check` | vet/test、依赖和工具配置校验 |
| `make snapshot` | 无 tag 的本地构建演练，不上传 |
| `make release VERSION=v0.4.0` | 要求 HEAD 对应同名 tag，构建校验本地资产，不上传 |
| `make publish VERSION=v0.4.0` | 显式上传 draft；默认由 CI 使用同一流程 |
| `make deploy-launchd VERSION=v0.4.0` | 独立的本地服务升级，显式运行才重启 |

注意：现有 `make release` 仍会发布，须等实现完成后才能按新语义使用。

## 安装与自更新

第一阶段提供 `install.sh` / `install.ps1`：

- macOS/Linux 默认 `~/.local/bin`，支持 `LLM_GATEWAY_INSTALL_DIR`；Windows 默认 `%LOCALAPPDATA%\llm-gateway`，幂等加入用户 PATH。
- `LLM_GATEWAY_VERSION=vX.Y.Z` 指定版本；未指定时查 latest stable，再固定本次下载 tag。
- 同时下载资产与 SHA256SUMS；缺文件、目标条目缺失/重复、哈希格式错误或不匹配都失败。
- 在目标目录创建临时文件，验证后替换；失败保留原文件并清理本次临时文件。Windows 覆盖失败须恢复旧文件。
- 不自动提权、不改 provider/token、不自动启动服务；显示实际安装路径和版本。
- 遇到其他安装路径或包管理器管理的安装时明确提示；不声称新路径已经替换了 launchctl 使用的路径。

第二阶段实现 `llm-gateway update [--check] [--version vX.Y.Z]`，借鉴 aivet 下载和校验模块。
`--check` 只查询；普通 update 更新当前安装位置，不自动重启服务或迁移数据库。
错误区分网络、资产缺失、checksum 与本地权限，返回非零；不把不可比较的开发版本判断成“最新”。
解析实际可执行路径与软链接，包管理器安装交由包管理器更新。
低版本安装须显式选择；降级二进制不等于降级数据库，不能承诺任意 `--version` 都能服务回滚。

## launchctl 升级与回滚

部署完成必须以新进程验证为准。磁盘上 version 变新或旧进程 `/healthz` 返回 200 都不足以证明成功。
此前写入 `/opt/homebrew/bin` 曾受限，权限预检应在停服前完成。

1. 从指定 plist/label 读取实际可执行路径、配置目录、服务域和地址，输出摘要过滤凭据。
2. 检查目标目录和服务控制权限，下载校验候选二进制并读取 version；失败不动现有服务。
3. SQLite 在线备份用于隔离迁移演练，不携带生产凭据自动探测上游。
4. 进入维护窗口，停止服务及其他写库进程（包括 MCP stdio），确认退出；保留 plist 供恢复。
5. 停写后做最终一致性备份，连同旧二进制、配置和加密密钥材料保存到权限受限目录。
   记录旧版本、schema、目标路径和备份位置；不能只复制活动 SQLite 主文件而遗漏 WAL。
6. 同目录原子替换二进制，再以原配置启动原 label；首次启动自动迁移数据库。
7. 轮询至超时，核对 launchctl 状态、新 PID、运行进程 version/commit/schema 和健康检查。
8. 鉴权后用 MCP `tools/list` 确认 `add_provider` 包含 `typesafe_base_url`，
   无效 System One 请求得到本地 400；这两项不产生上游调用。
9. 核对 provider/alias 数量和迁移完整性，通过后结束维护窗口并保留回滚材料。

`/healthz` 增加非敏感构建信息，保持原 status、uptime 和 HTTP 状态码语义。
版本通过显式参数注入服务，避免 server 包依赖 main。
此次 schema 为 v12 → v13；历史安装可能缺少新建库已有的表，必须用真实库副本演练，不能只测空库。

验证失败先停止新进程。只有已验证旧二进制兼容新 schema 才能仅回退二进制；否则恢复整套旧版本、
迁移前数据库和匹配密钥配置，再验证旧服务。恢复数据库会丢弃备份之后的写入，应在维护窗口内完成回滚；
恢复业务后不能无提示回灌旧库。权限受限时不自动换服务路径或覆盖 plist，报告“未部署，旧服务仍运行”。

## 实施拆分与验收

| 阶段 | 文件范围 | 必须通过的验收 |
| --- | --- | --- |
| P1 发布产物 | `.goreleaser.yaml`、release workflow、`Makefile`、资产校验脚本 | 平台矩阵、干净源码、tag/version 一致、校验和、draft 流程；不写安装目录 |
| P2 安装 | `install.sh`、`install.ps1`、安装文档 | 固定版本/latest、平台检测、坏 checksum、缺资产、断网、只读目录；失败不损坏旧文件 |
| P3 本地部署 | `deploy/launchd/`、`cmd/llm-gateway/start.go`、`internal/server/monitoring.go` | 权限预检、备份、真实库迁移、新 PID/版本验证、回滚演练 |
| P4 自更新 | `internal/selfupdate/`、`cmd/llm-gateway/update.go` | HTTP mock、版本判断、原子替换、Windows 失败恢复、安装路径识别；不隐式重启 |

P1–P3 形成首个可用发布闭环；P4 可后续迭代，不阻塞 TypeSafe 分发。
失败场景用临时目录、临时数据库、mock 下载服务器验证。Windows 替换行为需 Windows runner；
arm64 平台未完成运行验收的部分在候选版本记录中列明。

首版 Release notes 至少包含 Responses API 及流式修复、TypeSafe System One、缓存计费、用量汇总、
SQLite v13 迁移、TypeSafe 非聊天/非流式限制、API key 配置、launchctl 与 MCP 子进程重启说明。
附备份回滚步骤，分别列明 mock、真实上游联调、交叉编译与各平台运行验收结果。
