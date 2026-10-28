//go:build !windows

package main

import "errors"

// svcCommand 非 Windows 平台没有服务控制管理器，svc 子命令只报错不做事；
// 守护进程交给宿主（fnOS 插件 / DSM start-stop-status / systemd）管理。
func svcCommand(args []string) error {
	return errors.New("svc 子命令仅支持 Windows；本平台由宿主服务框架管理守护进程")
}
