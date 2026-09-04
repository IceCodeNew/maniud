# TUI 使用指南

`maniud tui` 是仓库设置、服务接入、dry-run 检查和 apply 的交互入口，无需额外的 TUI 参数。

## 使用前准备

开始前准备这些环境：

- Docker Engine、Podman，或者使用 containerd 的 nerdctl。
- Git 2.41 或更新版本。部署编辑通过 `--attr-source` 对照已经确认的 Git tree 计算实际提交的 blob。全局 name、email 和签名密钥配置完整时，`maniud` 会创建签名提交。
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

预览页会标识 runtime、所选镜像、服务名、Compose 路径，以及可能生成的准备脚本。较长的镜像身份会缩略显示。按 Enter 进入写文件确认页。选择执行后，`maniud` 只写入本次生成的文件，并暂存这些文件的精确路径。提交前，可以在 staged diff 中检查完整的生成值。

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

Review 页有 **Continue to confirmation** 和 **Explore options** 两个操作。按 Tab 或 Shift+Tab 移动焦点，再按 Enter 选择。按 `o` 可以直接打开 **Explore options**。选项页依次提供确定性解释、LLM 部署辅助、部署参数编辑和部署历史。

选择 **Edit deployment parameters** 可以打开编辑页。该页面列出支持的 CPU、内存、进程、生命周期和 healthcheck 字段，并显示当前值。输入新值，或者在允许删除的字段上按 `u`，即可恢复 Compose 默认值。编辑字段时按 Esc 返回字段列表，未保存的值仍作为草稿保留。离开编辑流程或退出程序时，界面会要求确认是否丢弃；选择 **Continue editing** 会保留草稿。

`maniud` 会先验证完整的内存 Compose source，再预先计算 Git 实际提交的 blob 和 diff，然后打开写入与暂存确认页。预览页只列出发生变化的字段，并排显示 Current 和 Proposed；过长的值会在中间缩略。按 `d` 可以打开只读 Details 页，查看完整值。确认页显示长度受限的 diff；按 `d` 可以打开完整的只读 diff 页面。该计算包含 `text`、`eol`、`working-tree-encoding` 和 `ident` 等 Git 内建 attributes 转换，并拒绝 external clean/process filter。暂存完成后，`maniud` 会再次核对 staged blob 和 diff 是否与确认内容相同，随后沿用服务接入流程的签名提交与显式未签名降级。Candidate 与 HEAD 相同时，界面会报告没有变化，也不会创建空提交。提交文件不会把变更应用到 runtime。

选择 **View deployment history** 可以查看当前 Compose 文件在 first-parent 历史中最近 100 个相关提交。History 页只说明提交中是否存在签名，不验证签名者。选择较早的文件版本后，`maniud` 会重新构建并验证 candidate；确认后创建新的 restore commit，不改写 Git 历史。当前文件版本不能创建无内容的 restore commit。

### 向 LLM 询问部署问题

在 **Explore options** 中选择 **Ask LLM about deployment**，即可配置 LLM assistance 或输入部署问题。配置 slides 支持 OpenAI、DeepSeek 和 OpenAI-compatible HTTPS endpoint，并依次收集 model、5–120 秒的单次请求超时与 API key。按 Esc 返回上一页时，界面会保留未保存的非敏感配置；离开配置流程或退出程序时，界面会要求确认是否丢弃。在 API key 页面按 Ctrl+D 会把现有 key 标记为待删除，留空则保留现有 key。输入页面中的 `q` 和 `c` 都是普通字符。保存只修改 `$XDG_CONFIG_HOME/maniud/.env`，不会连接 provider。

实际生效的配置完整时，流程会直接打开问题输入页。发送下一个问题前，可以在该页按 Ctrl+E 修改 provider 配置。

Maniud 按字段依次读取 process environment、当前目录的 `.env`、`$XDG_CONFIG_HOME/maniud/.env`。高优先级来源中的空 API-key assignment 会屏蔽低优先级 key。当前目录的文件包含 API key 时，文件必须属于当前用户，且 group 和 other 不能拥有权限。XDG 目录与文件分别使用 `0700` 和 `0600`，路径中不能含 symlink。

发送前，确认页会显示 provider、model、origin 和 key source。Provider 会收到长度受限的问题，以及包含受支持部署参数和少量 service、runtime、platform、action 元数据的 projection。Process environment、credential、私有路径、Compose 原文、runtime object ID 和完整镜像地址不会进入请求。问题中包含上述已知值时，Maniud 会在本地拒绝发送。

请求使用 non-streaming 模式，最多执行三次 HTTP attempt。Provider 可以回答当前问题、提出一个后续问题，或者给出类型明确的 Compose 修改建议。每个响应都必须通过严格的 JSON 和结构校验；修改建议还要通过字段、citation 和 Compose 参数校验。Provider 返回多个有效响应时，TUI 最多显示三个，并等待你明确选择，Maniud 不会代选。选择回答或后续问题后，TUI 返回问题页，并把所选内容加入临时会话记录。选择修改建议后，TUI 进入 Compose preview、暂存确认、diff 和 commit 流程。Provider 不能直接写入或提交文件。

保存结果的持久性无法确认时，TUI 会显示当前可见的非敏感配置和 key source。选择 **Retry Save** 只会重新写入受保护的配置，不会连接 provider。Provider 请求失败后，问题页会显示稳定 code、处理阶段、实际生效的 provider/model/origin、请求结果和下一步操作。请求结果分为尚未发起、provider 是否处理未知和已收到 HTTP 响应。后两种结果下重试可能再次计费。修改问题或 provider 配置后，界面会在下次确认前清除旧错误。会话接受八轮响应或达到文本上限后，再次发送当前问题即可开启不带旧记录的新会话。

Review 页支持这些操作：

- Tab 或 Shift+Tab 在 **Continue to confirmation** 和 **Explore options** 之间移动焦点。
- Enter 选择当前操作。
- `o` 直接打开 **Explore options**。等待 health convergence 时，该按键改为打开 health Details。
- `d` 打开 Details。
- `x` 导出当前 Details 内容并退出。
- `r` 重新执行 dry-run、snapshot 和 evidence。
- `?` 打开当前页面的快捷键帮助。
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

SIGKILL、断电或并发 Git 修改可能留下已经发布的文件或暂存改动，此时 `maniud` 无法证明它们属于哪次操作。本次 session 中的后续服务或部署修改会被阻止。请先按[Compose 编辑与 Git 结果](errors.zh-CN.md#compose-编辑与-git-结果)恢复，再重新运行 `maniud tui`。请保留生成的 `.name.yaml.swp` 文件：当该草稿是 checkout 中唯一的改动，并且仍与目标服务匹配时，Add service 流程会询问是否继续。

Daemon 会先从本地 journal 恢复持久化 apply transaction，再 fetch Git。正常处理时，失败或无效的 Compose source 只阻塞对应服务，daemon 会继续处理其他已注册服务。如果该 source 关联未完成的操作，daemon 会停止本轮处理，不 fetch Git，也不启动新的外部效果。[恢复与边界](recovery.zh-CN.md)记录了 transaction 状态和操作方式。
