package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"ruijie-auto-login/internal/ruijie"
)

type logoutResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`
}

func main() {
	fmt.Println("===================================")
	fmt.Println("       Ruijie Logout Tool")
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

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.RequestTimeout)*time.Second,
	)
	defer cancel()

	fmt.Println("正在获取当前登录会话...")

	userIndex, err := client.GetCurrentSession(ctx)
	if err != nil {
		fmt.Println("当前没有检测到登录会话。")
		return
	}

	fmt.Println("检测到当前登录会话。")
	fmt.Printf("userIndex: %s\n", userIndex)

	target := cfg.PortalBase +
		"/eportal/InterFace.do?method=logout"

	form := url.Values{}
	form.Set("userIndex", userIndex)

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		fmt.Printf("创建注销请求失败: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded; charset=UTF-8",
	)

	resp, err := client.HTTP.Do(req)
	if err != nil {
		fmt.Printf("发送注销请求失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		fmt.Printf("读取注销响应失败: %v\n", err)
		os.Exit(1)
	}

	var result logoutResponse

	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Printf("解析注销响应失败: %v\n", err)
		fmt.Printf("原始响应: %s\n", string(body))
		os.Exit(1)
	}

	if result.Result == "success" {
		fmt.Println()
		fmt.Println("注销成功。")
		return
	}

	fmt.Println()
	fmt.Printf("注销失败: %s\n", result.Message)
}
