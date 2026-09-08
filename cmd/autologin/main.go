package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ruijie-auto-login/internal/ruijie"
)

// printUsage 输出帮助信息。
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  autologin [options]")
	fmt.Println()
	fmt.Println("不带参数时：")
	fmt.Println("  持续监控在线状态，掉线后自动重新登录")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --status    查询当前在线状态")
	fmt.Println("  --logout    注销当前登录")
	fmt.Println("  --once      单次检查并登录，成功后退出")
	fmt.Println("  --help      显示帮助")
}

// newClient 加载配置并创建客户端。
func newClient() (*ruijie.Client, *ruijie.Config) {
	cfg, err := ruijie.LoadConfig("config.json")
	if err != nil {
		fmt.Printf("配置加载失败: %v\n", err)
		os.Exit(1)
	}

	cfg.PortalBase = ruijie.NormalizeBaseURL(cfg.PortalBase)

	client, err := ruijie.NewClient(cfg)
	if err != nil {
		fmt.Printf("初始化客户端失败: %v\n", err)
		os.Exit(1)
	}

	return client, cfg
}

// timeoutCtx 基于配置中的请求超时时间创建上下文。
func timeoutCtx(parent context.Context, cfg *ruijie.Config) (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		parent,
		time.Duration(cfg.RequestTimeout)*time.Second,
	)
}

// runStatus 查询当前认证状态后立即退出。
func runStatus(client *ruijie.Client, cfg *ruijie.Config) {
	ctx, cancel := timeoutCtx(context.Background(), cfg)
	defer cancel()

	user, loggedIn, err := client.GetCurrentUser(ctx)

	if err != nil {
		fmt.Printf("查询认证状态失败: %v\n", err)
		os.Exit(1)
	}

	if loggedIn {
		fmt.Println("当前状态: 在线")
		ruijie.PrintUserInfo(user)
		return
	}

	fmt.Println("当前状态: 离线")
}

// runLogout 注销当前登录后退出，不进入监控，不自动重新登录。
func runLogout(client *ruijie.Client, cfg *ruijie.Config) {
	ctx, cancel := timeoutCtx(context.Background(), cfg)
	defer cancel()

	fmt.Println("正在注销当前登录账号...")

	if err := client.Logout(ctx); err != nil {
		fmt.Printf("注销失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("注销成功。")
}

/*
	runOnce 单次认证模式，面向 iOS + iSH 场景：

	检查状态 → 已在线则直接退出；
	离线则获取登录参数并按账号轮询策略登录，
	登录成功后立即退出，不进入持续监控。
*/
func runOnce(client *ruijie.Client, cfg *ruijie.Config) {
	ctx, cancel := timeoutCtx(context.Background(), cfg)
	defer cancel()

	fmt.Println("正在检测当前登录状态...")

	user, loggedIn, err := client.GetCurrentUser(ctx)

	if err != nil {
		fmt.Printf("检测当前状态时发生错误: %v\n", err)
	}

	if loggedIn {
		fmt.Println("当前已经在线，无需重复登录。")
		ruijie.PrintUserInfo(user)
		return
	}

	fmt.Println("当前离线，开始尝试登录。")

	manager := ruijie.NewManager(client, cfg)

	// 保持项目现有策略：随机选择账号起点，之后按顺序轮询。
	manager.RandomStart()

	loginCtx, loginCancel := timeoutCtx(context.Background(), cfg)
	defer loginCancel()

	loginUser, success := manager.TryLoginCycle(loginCtx)

	if !success {
		fmt.Println("全部账号登录失败。")
		os.Exit(1)
	}

	fmt.Println("登录成功，本次认证结束。")
	ruijie.PrintUserInfo(loginUser)
}

func main() {
	statusFlag := flag.Bool("status", false, "查询当前在线状态")
	logoutFlag := flag.Bool("logout", false, "注销当前登录")
	onceFlag := flag.Bool("once", false, "单次检查并登录，成功后退出")
	helpFlag := flag.Bool("help", false, "显示帮助")

	flag.Usage = printUsage
	flag.Parse()

	if *helpFlag {
		printUsage()
		return
	}

	fmt.Println("===================================")
	fmt.Println("     Ruijie Campus Auto Login")
	fmt.Println("===================================")

	client, cfg := newClient()

	switch {
	case *statusFlag:
		runStatus(client, cfg)
		return

	case *logoutFlag:
		runLogout(client, cfg)
		return

	case *onceFlag:
		runOnce(client, cfg)
		return
	}

	// 默认模式：持续监控 + 掉线自动登录。
	manager := ruijie.NewManager(client, cfg)

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	fmt.Println()
	fmt.Println("正在检测当前登录状态...")

	checkCtx, checkCancel := timeoutCtx(ctx, cfg)

	user, loggedIn, err := client.GetCurrentUser(checkCtx)

	checkCancel()

	if err != nil {
		fmt.Printf("检测当前状态时发生错误: %v\n", err)
	}

	if loggedIn {
		fmt.Println()
		fmt.Println("检测到当前已经登录。")

		ruijie.PrintUserInfo(user)

		/*
			如果当前账号就在 config.json 中，
			后续掉线时就从它的下一个账号开始。
		*/
		if manager.MatchCurrentUser(user.UserID) {
			fmt.Printf(
				"当前账号位于账号列表第 %d 个\n",
				manager.CurrentIndex+1,
			)
		} else {
			fmt.Println("当前账号不在 config.json 中。")
			fmt.Println("掉线后将随机选择轮询起点。")
		}

		manager.Monitor(ctx)
		return
	}

	fmt.Println("当前未检测到登录账号。")

	// 首次启动随机选择账号。
	manager.RandomStart()

	for {
		loginCtx, loginCancel := timeoutCtx(ctx, cfg)

		_, success := manager.TryLoginCycle(loginCtx)

		loginCancel()

		if success {
			break
		}

		fmt.Printf(
			"\n全部账号登录失败，%d 秒后重新尝试。\n",
			cfg.RetryInterval,
		)

		select {
		case <-ctx.Done():
			return

		case <-time.After(
			time.Duration(cfg.RetryInterval) * time.Second,
		):
		}
	}

	manager.Monitor(ctx)
}
