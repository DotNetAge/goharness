//go:build unix || linux || darwin

package tools

import (
	"os/exec"
	"syscall"
	"time"
)

// bashWaitDelay 是命令被击杀后等待残留 I/O 管道关闭的上限。
// 到期后 cmd.Wait 强制返回，不再被持有管道的残留进程永久阻塞。
const bashWaitDelay = 5 * time.Second

// configureBashProcess 为 Bash 子进程配置进程组与超时击杀策略。
//
// 命令以独立进程组启动（Setpgid），超时取消时击杀整个进程组（含孙进程）：
// 仅杀直接子进程时，孙进程可继承 stdout/stderr 管道继续存活，令 cmd.Wait
// 永久阻塞（表现为 Bash 工具挂死 → 子代理卡死 → CollectResults 死等 →
// 主会话队列被永久占住）。WaitDelay 兜底：击杀后残留管道仍未关闭时，
// Wait 在延迟到期后强制返回并关闭管道。
func configureBashProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = bashWaitDelay
}
