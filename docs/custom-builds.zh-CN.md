# 构建自定义二进制文件

[English](custom-builds.md) | [简体中文](custom-builds.zh-CN.md)

发布版本同时包含 Docker、Podman 和 containerd。安装 Go 1.27 后，可以在源码检出目录中生成更小的二进制文件，只加入目标主机需要的运行时。

在仓库根目录运行构建器。多次使用 `--runtime` 可以选择多个运行时：

```sh
go run ./cmd/maniud-builder --runtime docker --output ./bin/maniud
go run ./cmd/maniud-builder --runtime docker --runtime podman --output ./bin/maniud
```

省略 `--runtime` 时，构建结果包含全部三种运行时。不需要连接容器运行时的命令可以关闭默认项：

```sh
go run ./cmd/maniud-builder --no-default-runtimes --output ./bin/maniud
```

`--target GOOS/GOARCH` 可以为[发布平台](release-verification.zh-CN.md)交叉编译：

```sh
go run ./cmd/maniud-builder --runtime docker --target linux/arm64 --output ./bin/maniud
```

构建器会先核对选中的运行时包和生成的二进制文件，再替换输出文件。JSON 构建清单包含输出路径、目标平台、运行时、Go 版本和源码提交。
