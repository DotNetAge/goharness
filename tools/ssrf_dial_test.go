package tools

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// V-23：拨号层必须拦截解析到私有/内部地址的目标。
//
// 注意：回环地址（127.0.0.0/8、::1）是**有意豁免**的——参见 isPrivateIP 里的
// 注释，mindx 作为单机桌面应用需要访问本机开发服务器与 CDP 调试端口。
// 因此这里只覆盖真正的私有/元数据网段，回环由下一条测试验证「确实放行」。
func TestSSRFDialContext_RejectsPrivateAddress(t *testing.T) {
	ctx := context.Background()
	cases := []string{
		"10.0.0.1:80",        // RFC1918
		"172.16.0.1:80",      // RFC1918
		"192.168.1.1:80",     // RFC1918
		"169.254.169.254:80", // 链路本地（AWS/GCP 元数据）
		"100.100.100.200:80", // CGNAT（阿里云元数据）
	}

	for _, addr := range cases {
		conn, err := ssrfDialContext(ctx, "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		if err == nil {
			t.Errorf("%s 应被拒绝，实际连接成功", addr)
			continue
		}
		if !strings.Contains(err.Error(), "私有") {
			t.Errorf("%s 应以「私有地址」拒绝，实际错误: %v", addr, err)
		}
	}
}

// 回环必须放行：起一个本机监听，验证拨号层真的能连上。
// 这同时也验证了「直连已校验 IP」的改动没有破坏正常拨号。
func TestSSRFDialContext_AllowsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("无法监听本机端口: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		c, err := ln.Accept()
		if err == nil {
			_ = c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := ssrfDialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("回环地址应被放行，实际失败: %v", err)
	}
	_ = conn.Close()
}

// 修复后拨号目标是「刚刚校验过的 IP 字面量」而非 hostname。
// 验证该路径仍能建立 TCP 连接——若直连 IP 写错（丢端口、IPv6 未加方括号），
// 会在这里暴露。联网测试，默认跳过：GOHARNESS_NET_TESTS=1 时启用。
func TestSSRFDialContext_DialsVerifiedIP(t *testing.T) {
	if os.Getenv("GOHARNESS_NET_TESTS") != "1" {
		t.Skip("联网测试：设置 GOHARNESS_NET_TESTS=1 启用")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := ssrfDialContext(ctx, "tcp", "example.com:443")
	if err != nil {
		t.Fatalf("公网目标拨号失败（直连已校验 IP 后应仍可连通）: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.RemoteAddr() == nil {
		t.Fatal("连接缺少 RemoteAddr")
	}
}
