# 错误参考

[English](errors.md) | [简体中文](errors.zh-CN.md)

每条失败的命令都会写入一个 JSON 对象，其中包含稳定的 `code`、消息和 `retryable` 值。
一般失败的进程退出状态为 1，操作取消时为 130。
默认结果不会包含私有路径、端点详情、凭据或上游响应正文。
`gen` 可能先在标准错误中打印便于阅读的处理提示，再写入 JSON 对象。

```json
{"code":"apply_failed","message":"apply failed","retryable":false}
```

| Code | 含义 | 处理方式 |
| --- | --- | --- |
| `invalid_input` | 命令或选定输入无效。 | 查看 `maniud COMMAND --help` 并修正输入。 |
| `operation_cancelled` | 系统信号或调用方取消了操作。 | 再次执行同一条命令，让 maniud 恢复未完成的操作。 |
| `generation_failed` | `gen` 无法验证镜像、服务或输出文件。 | 按标准错误中的提示处理，本地缺少镜像时先拉取，再重新运行 `gen`。 |
| `apply_failed` | `apply`、`daemon` 或 `doctor` 无法确认操作安全。 | 只有 `retryable` 为 true 时才直接重试，否则应保留当前状态并检查 `--debug` 结果。 |
| `health_pending` | 执行 Apply 或 Adopt 后，workload 已存在，但 healthcheck 仍在收敛。 | 再次执行相同命令，继续现有 healthcheck，不重复启动 workload。 |
| `health_degraded` | Workload 正在运行或已经停止，当前状态需要操作人员处理。 | 运行 `maniud tui`，检查当前证据，再确认该 transaction 提供的 health 操作。 |
| `runtime_not_built` | 当前二进制文件没有编译选定的运行时。 | 安装或构建包含该运行时的 maniud 二进制文件。 |
| `tui_unavailable` | `maniud tui` 缺少交互终端输入、终端输出或可用的 `TERM`。 | 改用交互终端运行；需要非交互验证时，运行 `maniud apply --dry-run <compose>`，结构化输出再加 `--json`。 |
| `export_failed` | 终端已经恢复，但标准输出没有接收完整的 TUI session 导出内容。 | 修复或重定向标准输出，重新运行 `maniud tui` 并再次请求导出。Maniud 不会自动重试写入。 |
| `internal_error` | 二进制文件无法提供选定的命令服务。 | 核验已安装的发布版本，并在报告问题时附上程序版本和 JSON 结果。 |

`retryable: true` 表示运行时、镜像仓库、速率限制或状态存储恢复后，相同输入可能成功。
`retryable` 为 false 时，应先修正输入或解决互相矛盾的证据，再重复执行命令。

## TUI 中的 Compose 来源诊断

已提交的 Compose 来源未通过验证时，`maniud tui` 会显示仓库相对路径和稳定的原因类别。YAML 解析器能安全确定位置时，界面也会显示行号和列号。路径超出屏幕范围时，详情页支持滚动查看，并且只提供一项修复操作。

TUI 不会显示配置值、原始 YAML、解析器消息、依赖库错误或绝对路径。`Position: Unavailable` 表示 Compose 验证在 YAML 解析之后发现问题，此时 maniud 无法确定可信的来源位置。

| Code | 问题和稳定原因 | 处理方式 |
| --- | --- | --- |
| `compose_source_invalid` | 选定文件未通过下表中的来源检查。 | 打开诊断页，按提示在 Maniud 外修复，再重新运行 `maniud tui`。 |
| `compose_source_not_found` | 新读取的仓库清单中没有已注册的 Compose 文件。 | 检查已注册仓库和相对文件路径，再重新打开服务清单。 |
| `compose_source_unavailable` | Maniud 无法读取足够的可信来源状态。 | 检查仓库访问权限和 Git 状态，再重新运行 `maniud tui`。 |
| `yaml_syntax_invalid` | YAML 解析器拒绝了文档语法。 | 在界面显示的位置修复语法，再重新运行 `maniud tui`。 |
| `yaml_structure_invalid` | 文档含有重复键，或 YAML 映射和值的结构无效。 | 在界面显示的位置修复结构，再重新运行 `maniud tui`。 |
| `yaml_feature_unsupported` | 文档使用了 Compose 边界无法安全保留的 YAML 功能。 | 改用支持的 YAML 写法，再重新运行 `maniud tui`。 |
| `compose_validation_failed` | 文档未通过支持的 Compose 合同，或缺少必需变量。 | 修复 Compose 字段或必需变量，再重新运行 `maniud tui`。 |

## 仓库初始化结果

以下 code 对应初始化 slides 中可恢复的失败。创建私有 GitHub 仓库会调用 `gh`；克隆或复用现有 Git 仓库不要求使用 GitHub。

| Code | 问题和稳定原因 | 处理方式 |
| --- | --- | --- |
| `repository_setup_invalid_input` | 仓库名、remote 或 checkout 路径无效。 | 检查仓库来源和 checkout 路径，再重试。 |
| `github_repository_create_failed` | `gh` 未能创建指定的私有仓库。 | 检查 `gh auth status` 和仓库名，再重试。 |
| `repository_clone_failed` | Git 无法把 remote 克隆到指定 checkout。 | 检查 remote 访问权限，并选择空目录或可复用的 checkout 路径。 |
| `repository_registration_failed` | Checkout 已存在，但 Maniud 无法将它注册为可信的 desired state。 | 检查 checkout 和其中已提交的 Compose 来源，再重试注册。 |
| `repository_setup_unavailable` | 初始化流程无法确认可信的仓库结果。 | 在 Maniud 外检查仓库，再重新运行 `maniud tui`。 |

## LLM assistance 结果

TUI 使用下列稳定 code 处理配置和 provider 失败。界面不会显示 provider body、依赖错误、credential、私有路径或被拒绝的值。

Provider 请求失败后，问题页还会显示处理阶段、实际生效的 provider/model/origin 和长度受限的请求结果。**The request was not sent** 表示 provider transport 尚未发起。**The request may have been processed or billed** 表示请求已经发起，但没有收到可信响应。**An HTTP response was received** 表示 provider 已经返回响应，后续本地校验或处理失败。后两种结果下重试可能产生新的计费请求。

| Code | 问题和稳定原因 | 处理方式 |
| --- | --- | --- |
| `llm_config_invalid` | 必填 provider 设置缺失，或格式、取值范围无效。 | 检查 provider、model、endpoint、timeout 和实际生效的 key。 |
| `llm_config_path_invalid` | 受保护的 XDG 配置路径含有 symlink、owner 错误或不安全的 mode。 | 移除 symlink，并把 `$XDG_CONFIG_HOME/maniud` 下目录和文件恢复为当前用户所有的 `0700`/`0600`。 |
| `config_reload_failed` | 保存前，Maniud 无法读取当前实际生效的配置。 | 修复当前配置的访问问题，重新加载后再保存。本次没有修改配置。 |
| `config_save_stale` | Slide 加载配置后，实际生效的配置又发生了变化。 | 重新加载当前配置，检查后再保存。 |
| `config_save_outcome_unknown` | 保存操作结束时无法确认受保护文件是否已经发布。 | 检查界面显示的配置和 key source，再选择 **Retry Save**。该操作不会连接 provider。 |
| `config_saved_reload_failed` | 受保护文件已经保存，但无法重新加载实际生效的值。 | 选择 **Reload LLM configuration**，不要重复保存。 |
| `llm_question_invalid` | 问题为空，或不符合本地文本和长度限制。 | 缩短或修正问题后重新发送。 |
| `llm_conversation_limit` | 会话达到轮数或字节数上限。 | 再次发送问题以开启新会话；新会话不会携带之前的对话记录。 |
| `llm_forbidden_value` | 问题中含有受保护的部署数据。 | 从问题中移除 credential、私有路径、完整镜像地址、command、port、mount、device 或 runtime ID。 |
| `llm_authentication_failed` | Provider 拒绝认证，或报告缺少 key。 | 检查实际生效的 API key。 |
| `llm_rate_limited` | Provider 报告速率或账户余额限制。 | 等待后再发送新的计费请求。 |
| `llm_context_limit` | Provider 因 context 过长而拒绝请求。 | 缩短问题。 |
| `llm_refused` | Provider 拒绝请求或触发 content filter。 | 修改问题。Maniud 不显示 provider refusal body。 |
| `llm_empty_response` | Provider 没有返回可用的 choice 或 content。 | 只有接受再次计费时才重新发送。 |
| `llm_truncated` | 输出达到长度上限后，provider 提前结束响应。 | 只有接受再次计费时才重新发送。 |
| `llm_invalid_response` | Response 未通过本地 choice schema、field、citation 或 value 检查。 | 修改问题或改用其他受支持 model；本次没有创建 candidate。 |
| `llm_model_unavailable` | Provider 找不到配置的 model。 | 检查 model 名称和 provider。 |
| `llm_timeout` | Provider request 超过配置的 deadline。 | 再次发送计费请求前，检查 timeout 和 provider 可用性。 |
| `llm_cancelled` | 当前 provider request 已取消。 | 需要发起新请求时返回问题页。 |
| `llm_provider_failed` | Provider 或 transport 失败没有匹配到更具体的类别。 | 再次发送计费请求前检查 provider 可用性。 |
| `llm_context_stale` | 请求进行时，Compose source 或实际生效的 provider 配置发生变化。 | 检查当前 Compose source 和配置后重新发送。 |

## Compose 编辑与 Git 结果

部署参数校验、history、暂存或提交失败且状态仍可确认时，TUI 会保留 draft。

| Code | 问题和稳定原因 | 处理方式 |
| --- | --- | --- |
| `compose_edit_precondition_failed` | 执行操作前，已确认的 Compose 来源或 Git 状态发生变化。 | 重新加载来源并检查新的 diff。 |
| `compose_edit_unsupported_source` | Editor 无法安全保留选定 Compose 来源的语义。 | 在 Maniud 外编辑并提交文件，再重新打开服务。 |
| `compose_edit_validation_failed` | 字段值或生成的 Compose candidate 未通过本地校验。 | 修正字段值，再请求新的预览。 |
| `compose_edit_publish_failed` | Maniud 恢复原始 Compose 文件后，暂存操作失败。 | 重新加载来源，再重试编辑。 |
| `compose_edit_worktree_unknown` | Maniud 无法确认已发布文件和 Git index 的状态。 | 检查 `git status --short`、`git diff` 和 `git diff --staged`；在 Maniud 外恢复 checkout，再重新运行 `maniud tui`。 |
| `git_commit_failed` | Git 未创建 commit，已暂存的部署编辑保持不变。 | 解决 Git 或签名问题，再重试同一个 commit。 |
| `parameter_history_unavailable` | Git 无法返回选定文件有界的 first-parent history。 | 检查仓库，再重新加载 History。 |
| `parameter_history_entry_invalid` | 选定 revision 已不能作为恢复来源。 | 重新加载 History，并选择当前条目。 |

`compose_edit_worktree_unknown` 会阻止后续服务或部署文件及 Git 修改、LLM recommendation 预览和 Apply，直到 checkout 恢复完成。

## 通知失败

Bark 通知需要设置 `BARK_DEVICE_KEY`。需要加密时，再设置 `BARK_ENCRYPTION_KEY`，它要与 Bark app 中的 16、24 或 32 字符 ASCII 密钥一致。Telegram 需要同时设置 `TELEGRAM_BOT_TOKEN` 和 `TELEGRAM_CHAT_ID`。

通知配置缺失或无效时，`apply` 或 `daemon start` 会在操作开始前停止并返回 `invalid_input`。以 `maniud notification: ...` 开头的发送问题属于诊断信息，不会改变操作结果或进程退出状态。不要因为通知失败而重复已经完成的操作。

## 调试输出

把 `--debug` 放在命令前：

```sh
maniud --debug apply --dry-run photos.yaml
```

调试输出有长度限制，并会移除环境变量值、包含凭据的 URL、Authorization 请求头、机密配置赋值和私钥。
结果仍可能包含非敏感的来源或运行时详情，对外发送前应先检查内容。

## 警告

警告不会改变成功命令的退出状态。

| Code | 含义 |
| --- | --- |
| `daemon_mount_probe_unavailable` | 运行时无法确认挂载位置的容量和文件系统身份，因此持久数据复制或恢复改用宿主机备份检查。 |
| `insecure_remote_engine` | `DOCKER_HOST` 使用未认证的明文 TCP，应通过运维方控制的 VPN 和防火墙限制该端点。 |
