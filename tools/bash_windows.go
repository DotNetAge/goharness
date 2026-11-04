//go:build windows

package tools

import (
	"os/exec"
	"time"
)

// bashWaitDelay 是命令被击杀后等待残留 I/O 管道关闭的上限。
// 到期后 cmd.Wait 强制返回，不再被持有管道的残留进程永久阻塞。
const bashWaitDelay = 5 * time.Second

// configureBashProcess（Windows）：无进程组击杀语义，仅配置等待延迟兜底——
// 取消后残留 I/O 管道未关闭时，cmd.Wait 在延迟到期后强制返回并关闭管道。
func configureBashProcess(cmd *exec.Cmd) {
	cmd.WaitDelay = bashWaitDelay
}
