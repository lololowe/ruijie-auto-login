package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ruijie-auto-login/internal/ruijie"
)

func main() {
	fmt.Println("===================================")
	fmt.Println("     Ruijie Campus Auto Login")
	fmt.Println("===================================")

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

	manager := ruijie.NewManager(client, cfg)

	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer cancel()

	fmt.Println()
	fmt.Println("正在检测当前登录状态...")

	checkCtx, checkCancel := context.WithTimeout(
		ctx,
		time.Duration(cfg.RequestTimeout)*time.Second,
	)

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
		loginCtx, loginCancel := context.WithTimeout(
			ctx,
			time.Duration(cfg.RequestTimeout)*time.Second,
		)

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
