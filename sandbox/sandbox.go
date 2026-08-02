package sandbox

import (
	"fmt"
	"sync/atomic"

	"github.com/DotNetAge/goharness/logging"
)

// Sandbox 是会话级逻辑沙箱，挂载在 Session 上。
//
// 它是"决策层"而非"执行环境"——通过统一的 CheckXxx 方法为工具提供
// 文件、网络、命令的安全决策，工具根据决策决定是否执行。
//
// 线程安全：policy 用 atomic.Pointer 保护，可在运行时原子替换。
// 适合在多 Agent 并发的 ReAct 循环中被并发调用。
//
// 设计哲学：Go 简洁思想，够用就好。
//   - 不引入接口抽象层（Sandbox 直接是结构体）
//   - 不预留未来可能用不到的扩展点
//   - 不实现文档里提到但当前不需要的功能（如 DeniedEnvKeys、数据流追踪）
type Sandbox struct {
	policy atomic.Pointer[SandboxPolicy]
	logger logging.Logger
}

// NewSandbox 创建沙箱实例。
//
// 参数：
//   - policy: 初始策略（必传，不能为 nil）
//   - logger: 日志记录器（必传，不能为 nil）
//
// 返回：
//   - 沙箱实例
//   - 策略校验失败时返回 error
//
// 符合项目硬性约束：构造函数返回 error，必传参数非空检查。
func NewSandbox(policy *SandboxPolicy, logger logging.Logger) (*Sandbox, error) {
	if policy == nil {
		return nil, fmt.Errorf("创建沙箱失败: policy 不能为 nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("创建沙箱失败: logger 不能为 nil")
	}

	compiled, err := policy.Compile()
	if err != nil {
		return nil, fmt.Errorf("创建沙箱失败: %w", err)
	}

	sb := &Sandbox{logger: logger}
	sb.policy.Store(&compiled)
	return sb, nil
}

// Policy 返回当前生效的策略快照（值拷贝，调用方修改不影响沙箱）。
func (s *Sandbox) Policy() SandboxPolicy {
	p := s.policy.Load()
	if p == nil {
		return SandboxPolicy{}
	}
	return *p
}

// UpdatePolicy 原子替换策略。
// 已活跃的会话在下次决策时读到新策略，无需重启。
func (s *Sandbox) UpdatePolicy(policy *SandboxPolicy) error {
	if policy == nil {
		return fmt.Errorf("更新策略失败: policy 不能为 nil")
	}
	compiled, err := policy.Compile()
	if err != nil {
		return fmt.Errorf("更新策略失败: %w", err)
	}
	s.policy.Store(&compiled)
	if s.logger != nil {
		s.logger.Info("沙箱策略已更新")
	}
	return nil
}

// Logger 返回沙箱的日志记录器（供集成层访问）。
func (s *Sandbox) Logger() logging.Logger {
	return s.logger
}
