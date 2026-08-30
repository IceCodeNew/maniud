# TUI 使用指南

`maniud tui` 是仓库设置、服务接入、dry-run 检查和 apply 的交互入口，无需额外的 TUI 参数。

## 使用前准备

开始前准备这些环境：

- Docker Engine、Podman，或者使用 containerd 的 nerdctl。
- Git。全局 name、email 和签名密钥配置完整时，`maniud` 会创建签名提交。
- 目标镜像的仓库访问权限。接入服务前，先把固定 digest 的镜像拉取到选定 runtime。
- 最小 32×8 的交互终端。80×24 及以上会显示完整布局。

运行：

```sh
maniud tui
```

## 设置期望状态仓库

尚未注册仓库时，第一页会预填 `$HOME/maniud-desired-state`。你可以修改路径，再按 Enter 检查。确认页默认选中 **Back**，按 Tab 或方向键选择 **Set up**，然后按 Enter。

设置流程会创建使用 `master` 分支的本地 Git 仓库，并把注册信息写入 maniud 状态目录。它不会创建远端仓库。执行接入流程结束时打印的 push 命令前，请先添加 `origin` remote。

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

Review 页对比当前镜像身份和建议镜像身份。较长的镜像地址仍保留在并排表格中，中间部分会缩略。按 `d` 可以在 Details 页查看完整内容。

Review 页支持这些操作：

- Enter 打开 apply 确认页。
- `d` 打开 Details。
- `r` 重新执行 dry-run、snapshot 和 evidence。
- Esc 返回服务选择。
- 没有操作执行时，`q` 退出。

Apply 确认页默认选中 **Back**。确认 runtime 变更无误后，选择 **Apply** 并按 Enter。`maniud` 执行一次事务 apply，完成后刷新 evidence。

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

Daemon 会先从本地 journal 恢复持久化 apply transaction，再 fetch Git。失败或无效的 Compose source 只阻塞对应服务，daemon 会继续处理其他已注册服务。[恢复与边界](recovery.zh-CN.md)记录了 transaction 状态和操作方式。
