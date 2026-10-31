//go:build windows

package main

// svc 子命令：把 tad-module 注册成 Windows 服务（SCM 托管、开机自启、崩溃自动
// 拉起）。交互式运行不需要它——直接用 serve 子命令即可。
//
// 用法:
//
//	tad-module svc install [serve 参数...]   安装服务（缺省参数见 defaultServeArgs）
//	tad-module svc remove                    停止并删除服务
//	tad-module svc start                     启动服务
//	tad-module svc stop                      停止服务
//	tad-module svc run [serve 参数...]       SCM 内部入口，勿手动调用
//
// 所有命令都需要管理员权限（服务控制管理器要求）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	svcName        = "tad-module"
	svcDisplayName = "TAD6S4N 模块 (tad-module)"
	svcDescription = "TAD6S4N10G 机箱功耗、风扇、存储与按键监控模块（Windows 版）。"
	svcHTTPAddr    = "127.0.0.1:8123"
)

func svcCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tad-module svc <install|remove|start|stop|run> [serve options...]")
	}
	switch args[0] {
	case "install":
		return svcInstall(args[1:])
	case "remove":
		return svcRemove()
	case "start":
		return svcStart()
	case "stop":
		return svcStop()
	case "run":
		return svcRun(args[1:])
	default:
		return fmt.Errorf("未知的 svc 子命令 %q", args[0])
	}
}

// defaultServeArgs 标准目录布局的缺省 serve 参数：数据在 %ProgramData%\tad-module，
// 静态资源取 exe 同级的 web\，只监听回环地址。
func defaultServeArgs() ([]string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	dataDir := filepath.Join(os.Getenv("ProgramData"), svcName)
	return []string{
		"--web-root", filepath.Join(filepath.Dir(exePath), "web"),
		"--config", filepath.Join(dataDir, "config.json"),
		"--state", filepath.Join(dataDir, "state.json"),
		"--log", filepath.Join(dataDir, "tad-module.log"),
		"--http", svcHTTPAddr,
		"--base-path", "/",
	}, nil
}

// quoteArg 生成可安全拼进服务 ImagePath 的参数：含空格的 token 用引号包住
// （mgr.CreateService 直接空格拼接，不做引号处理）。
func quoteArg(token string) string {
	if strings.ContainsAny(token, " \t") {
		return `"` + token + `"`
	}
	return token
}

// ensureServeDirs 预建 --config/--state/--log 指向的目录：服务首次启动时
// LoadOrCreateConfig 写配置不建父目录，缺目录会把服务带进崩溃循环。
func ensureServeDirs(args []string) {
	for i, arg := range args {
		var path string
		switch arg {
		case "--config", "--state", "--log":
			if i+1 < len(args) {
				path = args[i+1]
			}
		}
		if path != "" {
			if dir := filepath.Dir(path); dir != "" {
				_ = os.MkdirAll(dir, 0o755)
			}
		}
	}
}

func svcInstall(args []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if len(args) == 0 {
		args, err = defaultServeArgs()
		if err != nil {
			return err
		}
	}
	ensureServeDirs(args)

	connect, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM（需要管理员权限）: %w", err)
	}
	defer connect.Disconnect()
	if service, err := connect.OpenService(svcName); err == nil {
		service.Close()
		return errors.New("服务已存在，先执行 tad-module svc remove 再安装")
	}
	config := mgr.Config{
		StartType:   mgr.StartAutomatic,
		DisplayName: svcDisplayName,
		Description: svcDescription,
	}
	binArgs := append([]string{"svc", "run"}, args...)
	service, err := connect.CreateService(svcName, exePath, config, binArgs...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer service.Close()
	// 崩溃自动拉起：10s/30s 两次重启，之后交还 SCM 默认处理（24h 窗口）。
	_ = service.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)

	quoted := make([]string, 0, len(binArgs))
	for _, arg := range binArgs {
		quoted = append(quoted, quoteArg(arg))
	}
	fmt.Printf("服务 %s 已安装\n  程序: %s %s\n", svcName, exePath, strings.Join(quoted, " "))
	fmt.Printf("启动: tad-module svc start   网页: http://%s/\n", svcHTTPAddr)
	return nil
}

func svcRemove() error {
	connect, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM（需要管理员权限）: %w", err)
	}
	defer connect.Disconnect()
	service, err := connect.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", svcName, err)
	}
	defer service.Close()
	if status, err := service.Query(); err == nil && status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err != nil {
			fmt.Printf("停止服务失败（继续删除）: %v\n", err)
		} else if err := waitServiceStopped(service, 30*time.Second); err != nil {
			fmt.Printf("等待服务停止超时（继续删除）: %v\n", err)
		}
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Printf("服务 %s 已删除\n", svcName)
	return nil
}

func svcStart() error {
	connect, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM（需要管理员权限）: %w", err)
	}
	defer connect.Disconnect()
	service, err := connect.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", svcName, err)
	}
	defer service.Close()
	if err := service.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	fmt.Printf("服务 %s 已启动   网页: http://%s/\n", svcName, svcHTTPAddr)
	return nil
}

func svcStop() error {
	connect, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect SCM（需要管理员权限）: %w", err)
	}
	defer connect.Disconnect()
	service, err := connect.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", svcName, err)
	}
	defer service.Close()
	if _, err := service.Control(svc.Stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	if err := waitServiceStopped(service, 30*time.Second); err != nil {
		return err
	}
	fmt.Printf("服务 %s 已停止\n", svcName)
	return nil
}

func waitServiceStopped(service *mgr.Service, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timeout waiting for service to stop")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// svcRun SCM 启动服务的入口（ImagePath 带 "svc run <serve 参数>"）。
// 服务控制管理器内运行时没有控制台，Ctrl+C 信号无从谈起——Stop 请求由
// serviceHandler 转成 context 取消。
func svcRun(args []string) error {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("detect service context: %w", err)
	}
	if !inService {
		return errors.New("svc run 只能由 Windows 服务控制管理器启动；交互式运行请直接使用 serve 子命令")
	}
	ensureServeDirs(args)
	return svc.Run(svcName, &serviceHandler{args: args})
}

type serviceHandler struct {
	args []string
}

func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending, WaitHint: 15000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, h.args) }()

	for {
		select {
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				// Restore（恢复 BIOS 功耗/风扇状态）可能耗时，给足 WaitHint
				status <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()
			}
		case err := <-done:
			status <- svc.Status{State: svc.StopPending}
			if err != nil {
				// 让 SCM 的恢复动作（重启服务）接手；错误已由 runServe 记日志
				return false, 1
			}
			return false, 0
		}
	}
}
