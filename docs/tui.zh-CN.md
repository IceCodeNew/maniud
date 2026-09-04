# TUI 使用指南

`maniud tui` 是仓库设置、服务接入、dry-run 检查和 apply 的交互入口，无需额外的 TUI 参数。

## 使用前准备

开始前准备这些环境：

- Docker Engine、Podman，或者使用 containerd 的 nerdctl。
- Git。全局 name、email 和签名密钥配置完整时，`maniud` 会创建签名提交。
- 只有让 `maniud` 创建私有 GitHub 仓库时才需要 GitHub CLI（`gh`）。请先安装并登录，确保 `gh auth status` 执行成功；否则请改用现有 Git 仓库。
- 目标镜像的仓库访问权限。接入服务前，先把固定版本或 digest 的镜像拉取到选定 runtime。
- 最小 32×8 的交互终端。80×24 及以上会显示完整布局。

运行：

```sh
maniud tui
```

## 设置期望状态仓库

尚未注册仓库时，请选择仓库来源：

- **Create private GitHub repository** 接受 `OWNER/REPOSITORY`，调用 `gh` 创建仓库，再克隆到你指定的本地路径。
- **Use existing Git repository** 接受 HTTPS 或 file remote，通过 Git 克隆仓库，或者复用指定路径中的干净 checkout。该模式不调用 `gh`，也不要求仓库托管在 GitHub。

本地 checkout 页面会预填 `$HOME/maniud-desired-state`。你可以修改路径，再按 Enter 检查来源和路径。确认页默认选中 **Back**，按 Tab 或方向键选择设置操作，然后按 Enter。设置完成后，Maniud 会把注册信息写入状态目录。

注册缺失时，你可以从首页重新进入仓库设置。按 Esc 跳过设置后，仍可通过路径检查 Compose 文件。

## 添加服务

在首页选择 **Add service**，输入下面两类内容之一：

- 固定版本或 digest 的镜像 URI。
- 完整的 `docker`、`podman` 或 `nerdctl` `run`/`create` 命令。

`maniud` 把 runtime 命令作为配置输入解析，不会执行你粘贴的命令。目标镜像在仓库和本地 runtime 中需要解析为同一身份。

预览页会显示 runtime、精确镜像身份、服务名、Compose 路径，以及可能生成的准备脚本。按 Enter 进入写文件确认页。选择执行后，`maniud` 只写入本次生成的文件，并暂存这些文件的精确路径。

## 检查并提交生成文件

提交页显示长度受限的 diff 和建议的提交说明，可使用这些按键：

- Up 和 Down 滚动 diff。
- `d` 打开完整 diff 页面。
- `e` 编辑提交说明。
- Tab、Shift+Tab、Left 或 Right 在 **Back** 和 **Commit** 之间切换。

`maniud` 会先读取你的全局 Git 身份并请求签名提交。如果签名失败，且暂存文件没有变化，界面会打开单独的确认页，允许创建未签名提交。你必须主动选择这个降级操作，提交使用的暂存树不会变化。

提交成功后，`maniud` 会重新读取刚提交的 source，再依次执行 dry-run、snapshot 和 evidence 检查，然后进入 Review 页。

## 检查并应用变更

Review 页对比当前镜像身份和建议镜像身份。较长的镜像地址仍保留在并排表格中，中间部分会缩略。按 `d` 可以在 Details 页查看完整值和本次 session 的时间线。

按 `e` 可以编辑部署参数。编辑页列出支持的 CPU、内存、进程、生命周期和 healthcheck 字段，并显示当前值。输入新值，或者在允许删除的字段上按 `u`，即可恢复 Compose 默认值。`maniud` 会先在内存中验证完整 Compose source，再打开独立的写入和暂存确认页。提交页显示实际 staged diff，并沿用服务接入流程的签名提交与显式未签名降级。提交文件不会把变更应用到 runtime。

按 `h` 可以查看当前 Compose 文件在 first-parent 历史中最近 100 个相关提交。History 页只说明提交中是否存在签名，不验证签名者。选择较早的文件版本后，`maniud` 会重新构建并验证 candidate；确认后创建新的 restore commit，不改写 Git 历史。当前文件版本不能创建无内容的 restore commit。

Review 页支持这些操作：

- Enter 打开 apply 确认页。
- `e` 打开部署参数编辑页。
- `h` 打开部署历史。
- `d` 打开 Details。
- `x` 导出当前 Details 内容并退出。
- `r` 重新执行 dry-run、snapshot 和 evidence。
- Esc 返回服务选择。
- 没有操作执行时，`q` 退出。

Apply 确认页默认选中 **Back**。确认 runtime 变更无误后，选择 **Apply** 并按 Enter。`maniud` 执行一次事务 apply，完成后刷新 evidence。

Details 页记录当前 TUI session 的 application observation，最多保留 128 项或 64 KiB，先达到任一限制后停止追加并标记 truncated。页面也会显示事件丢弃数量。这些 observation 只提供诊断信息，不会切换页面、判定操作成功或触发 runtime 变更。

Review 和 Details 页在没有操作执行且终端不小于 56×16 时支持导出。Bubble Tea 恢复终端后，`maniud` 会向标准输出完整写入一次纯文本内容，其中包括未缩略的当前镜像身份、建议镜像身份和长度受限的时间线。导出内容不含提问、回答、候选原文、API key、原始错误、响应正文、代理路由或未保存草稿。Maniud 默认不持久化时间线。

## 打开已有服务

首页列出已注册仓库中的有效 Compose 文件。选择服务后，`maniud` 会创建新的只读 snapshot 并显示当前状态。

选择 **Open Compose file** 可以输入另一个已经提交的 Compose 路径。文件必须位于干净的 Git checkout 中，并且可以解析到一个 commit，apply 请求才会保留 source provenance。

无效 Compose 文件会作为 blocked 条目出现，不会阻止目录中其他服务加载。选择 blocked 条目可以查看稳定的 source diagnostic。请在 TUI 外修复文件，退出并重新运行 `maniud tui`，它会重新构建目录。

## 退出后打印的命令

有些服务需要准备由 root 拥有的 bind 路径。`maniud` 不会在 TUI 中执行 `sudo`。退出 alternate screen 后，它会先打印准备命令（如果存在），再打印 Git push 命令和 `maniud tui`。

请先检查每条命令。Push 需要仓库已经配置 `origin` remote。完成准备或 push 后重新进入 TUI，它会读取新的仓库状态。

## 终端行为

界面按终端尺寸切换布局：

| 终端尺寸 | 界面行为 |
| --- | --- |
| 不小于 80×24 | 完整步骤栏和并排 Review 表格 |
| 不小于 56×16 | 保留相同操作的紧凑内容 |
| 不小于 32×8 | 最小安全操作界面 |
| 更小 | 只显示调整尺寸提示 |

设置 `NO_COLOR` 可以禁用颜色。设置 `MANIUD_TUI_ASCII=1` 可以把 Unicode 标记替换成 ASCII。标准输入或输出不是 TTY，或者 `TERM=dumb` 时，`maniud tui` 会返回 `tui_unavailable`，不会修改文件或 runtime 状态。

非交互检查可以使用：

```sh
maniud apply --dry-run path/to/compose.yaml
maniud apply --dry-run --json path/to/compose.yaml
```

## 取消与恢复

按 Ctrl+C 可以取消 session。如果操作已经跨过外部效果边界，`maniud` 会等待操作进入稳定状态再退出。操作进行中按 `q` 也采用相同处理。

SIGKILL、断电或并发 Git 修改可能留下已经发布的文件或暂存改动，此时 `maniud` 无法证明它们属于哪次操作。下次进入 TUI 时，程序会阻止编辑和 apply。请检查 `git status --short`、`git diff` 和 `git diff --staged`，再手动完成或恢复仓库。请保留生成的 `.name.yaml.swp` 文件：当该草稿是 checkout 中唯一的改动，并且仍与目标服务匹配时，Add service 流程会询问是否继续。

Daemon 会先从本地 journal 恢复持久化 apply transaction，再 fetch Git。失败或无效的 Compose source 只阻塞对应服务，daemon 会继续处理其他已注册服务。[恢复与边界](recovery.zh-CN.md)记录了 transaction 状态和操作方式。
