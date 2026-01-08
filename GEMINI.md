# Fast Sandbox 项目工程准则 (GEMINI.md)

本文档定义了 AI 助手在开发 Fast Sandbox 项目时必须遵守的工程原则和流程。

## 1. E2E 测试开发准则 (E2E Testing Standards)

### 1.1 独立性原则
- **独立目录**: 每个 E2E 测试场景必须位于 `test/e2e/` 下的独立子目录中。
- **命名规范**: 子目录必须使用描述性命名（例如 `sandbox-lifecycle-expiry` 而不是 `test1`）。
- **自包含资源**: 测试专用的 YAML 模板必须放在该 Case 的目录内。

### 1.2 CRD 管理
- **唯一来源**: 项目的 CRD 定义必须且仅能存在于根目录的 `config/crd/` 下。
- **禁止副本**: 禁止在测试子目录中创建或存储 CRD 的 YAML 副本。测试脚本必须直接指向 `config/crd/` 下的文件进行安装。

### 1.3 脚本生命周期
每个测试脚本（`.sh`）必须实现以下完整闭环：
1.  **环境初始化**: 调用 `common.sh` 中的 `setup_env`。
2.  **前置准备**: 调用 `common.sh` 中的 `install_infra`。
3.  **核心测试**: 执行业务逻辑验证。
4.  **环境清理**: 无论结果如何，必须在 `trap` 中调用 `cleanup_all`。

### 1.4 代码复用 (Dry Principle)
- **公共库**: 所有通用的初始化（镜像构建/导入）、基础组件安装（CRD/Controller/Janitor）、以及工具函数（wait_for_pod）必须抽象到 `test/e2e/common.sh` 中。
- **调用规范**: 单个 Case 脚本应通过 `source ../common.sh` 引用这些能力，严禁在脚本中硬编码重复的 `make` 或 `kubectl delete` 逻辑。

## 2. 代码开发准则

### 2.1 镜像构建
- 优先使用多阶段构建（Dockerfile）以减小镜像体积（目标 < 50MB）。
- 确保 Makefile 中的 `docker-*` 目标与测试脚本中的调用逻辑一致。

### 2.2 隔离哲学
- 始终考虑嵌套容器环境（KIND）下的兼容性。
- 在 Cgroup 限制无法应用时，必须提供软限额（环境变量）作为降级方案。
