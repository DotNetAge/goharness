<div align="center">

# goharness

**Go 语言的 AI Agent 运行时（Harness）框架。**

[![Go Report Card](https://goreportcard.com/badge/github.com/DotNetAge/goharness)](https://goreportcard.com/report/github.com/DotNetAge/goharness)
[![Go Version](https://img.shields.io/badge/go-1.25+-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

[**Website**](https://goharness.rayainfo.cn) | [**English**](./README.md) | [**中文说明**](./README_zh-CN.md)

</div>

---

> **中文说明**：本仓库的主文档（[README.md](./README.md)）即以中文撰写，包含：
>
> - **goharness 是什么**：定位、依赖分层（宿主 → goharness → goagent → gochat）
> - **核心特性**：goagent 内核驱动、统一沙箱安全、SubAgent 异步编排、中断-恢复交互、全链路事件流、Hook 扩展、上下文工程、SPI 收窄设计、渐进式技能加载、模型无关
> - **快速开始**：最小示例、事件订阅、自定义工具、沙箱策略
> - **SubAgent 编排**：派发与收集、授权与提问冒泡
> - **内置工具清单**、**Runtime 配置选项**、**宿主集成 SPI**
>
> 请直接阅读 [README.md](./README.md)。
