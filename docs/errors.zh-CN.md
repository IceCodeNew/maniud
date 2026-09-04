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
| `runtime_not_built` | 当前二进制文件没有编译选定的运行时。 | 安装或构建包含该运行时的 maniud 二进制文件。 |
| `tui_unavailable` | `maniud tui` 缺少交互终端输入、终端输出或可用的 `TERM`。 | 改用交互终端运行；需要非交互验证时，运行 `maniud apply --dry-run <compose>`，结构化输出再加 `--json`。 |
| `export_failed` | 终端已经恢复，但标准输出没有接收完整的 TUI session 导出内容。 | 修复或重定向标准输出，重新运行 `maniud tui` 并再次请求导出。Maniud 不会自动重试写入。 |
| `internal_error` | 二进制文件无法提供选定的命令服务。 | 核验已安装的发布版本，并在报告问题时附上程序版本和 JSON 结果。 |

`retryable: true` 表示运行时、镜像仓库、速率限制或状态存储恢复后，相同输入可能成功。
`retryable` 为 false 时，应先修正输入或解决互相矛盾的证据，再重复执行命令。

## TUI 中的 Compose 来源诊断

已提交的 Compose 来源未通过验证时，`maniud tui` 会显示仓库相对路径和稳定的原因类别。YAML 解析器能安全确定位置时，界面也会显示行号和列号。路径超出屏幕范围时，你可以在详情页滚动查看；详情页只提供一项修复操作。

TUI 不会显示配置值、原始 YAML、解析器消息、依赖库错误或绝对路径。`Position: Unavailable` 表示 Compose 验证在 YAML 解析之后发现问题，此时 maniud 无法确定可信的来源位置。

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
