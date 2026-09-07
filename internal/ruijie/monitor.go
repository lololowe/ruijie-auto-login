package ruijie

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

type Manager struct {
	Client *Client
	Config *Config

	CurrentIndex int
	RNG          *rand.Rand
}

func NewManager(client *Client, cfg *Config) *Manager {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	return &Manager{
		Client:       client,
		Config:       cfg,
		CurrentIndex: -1,
		RNG:          rng,
	}
}

// RandomStart 随机选择一个账号作为第一次尝试的起点。
func (m *Manager) RandomStart() {
	if len(m.Config.Accounts) == 0 {
		m.CurrentIndex = -1
		return
	}

	m.CurrentIndex = m.RNG.Intn(len(m.Config.Accounts))

	fmt.Printf(
		"随机选择账号起点: %d/%d\n",
		m.CurrentIndex+1,
		len(m.Config.Accounts),
	)
}

// MatchCurrentUser 尝试把当前登录账号和配置中的账号对应起来。
func (m *Manager) MatchCurrentUser(userID string) bool {
	for i, account := range m.Config.Accounts {
		if account.Username == userID {
			m.CurrentIndex = i
			return true
		}
	}

	return false
}

// NextAccount 移动到下一个账号。
func (m *Manager) NextAccount() {
	if len(m.Config.Accounts) == 0 {
		m.CurrentIndex = -1
		return
	}

	m.CurrentIndex++

	if m.CurrentIndex >= len(m.Config.Accounts) {
		m.CurrentIndex = 0
	}
}

// TryLoginCycle 从当前账号开始，按顺序尝试一整圈。
func (m *Manager) TryLoginCycle(ctx context.Context) (*UserInfo, bool) {
	accountCount := len(m.Config.Accounts)

	if accountCount == 0 {
		return nil, false
	}

	if m.CurrentIndex < 0 || m.CurrentIndex >= accountCount {
		m.RandomStart()
	}

	for tried := 0; tried < accountCount; tried++ {
		account := m.Config.Accounts[m.CurrentIndex]

		fmt.Printf(
			"\n正在尝试账号 [%d/%d]: %s\n",
			m.CurrentIndex+1,
			accountCount,
			account.Username,
		)

		user, err := m.Client.LoginAndVerify(ctx, account)

		if err == nil && user != nil {
			fmt.Printf("登录成功\n")
			fmt.Printf("账号: %s\n", user.UserID)
			fmt.Printf("IP: %s\n", user.UserIP)

			m.MatchCurrentUser(user.UserID)

			return user, true
		}

		if err != nil {
			fmt.Printf("登录失败: %v\n", err)
		}

		m.NextAccount()
	}

	return nil, false
}

// PrintUserInfo 打印当前登录账号。
func PrintUserInfo(user *UserInfo) {
	fmt.Println()
	fmt.Println("========== 当前登录状态 ==========")
	fmt.Printf("状态: 已登录\n")
	fmt.Printf("账号: %s\n", user.UserID)
	fmt.Printf("用户名: %s\n", user.UserName)
	fmt.Printf("IP: %s\n", user.UserIP)
	fmt.Printf("MAC: %s\n", user.UserMAC)
	fmt.Printf("服务: %s\n", user.Service)
	fmt.Printf("登录类型: %s\n", user.LoginType)
	fmt.Printf("Portal IP: %s\n", user.PortalIP)
	fmt.Printf("Portal URL: %s\n", user.PortalURL)
	fmt.Println("==================================")
}

// Monitor 持续监测网络与登录状态。
func (m *Manager) Monitor(ctx context.Context) {
	ticker := time.NewTicker(
		time.Duration(m.Config.CheckInterval) * time.Second,
	)
	defer ticker.Stop()

	fmt.Printf(
		"\n开始监控，检测间隔: %d 秒\n",
		m.Config.CheckInterval,
	)

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(
				ctx,
				time.Duration(m.Config.RequestTimeout)*time.Second,
			)

			user, loggedIn, err := m.Client.GetCurrentUser(checkCtx)

			internetOK := m.Client.CheckInternet(checkCtx)

			cancel()

			if err != nil {
				fmt.Printf(
					"[监控] 查询登录状态失败: %v\n",
					err,
				)
			}

			if loggedIn && internetOK {
				fmt.Printf(
					"[监控] 在线 | 账号: %s | IP: %s\n",
					user.UserID,
					user.UserIP,
				)

				m.MatchCurrentUser(user.UserID)

				continue
			}

			fmt.Println()
			fmt.Println("========== 检测到连接异常 ==========")

			if !loggedIn {
				fmt.Println("当前状态: 未登录")
			} else {
				fmt.Println("当前状态: Portal 会话异常")
			}

			if !internetOK {
				fmt.Println("互联网状态: 不可用")
			} else {
				fmt.Println("互联网状态: 可用")
			}

			fmt.Println("准备尝试下一个账号...")
			fmt.Println("====================================")

			/*
				当前账号掉线后，直接从下一个账号开始。
			*/
			m.NextAccount()

			loginCtx, cancel := context.WithTimeout(
				ctx,
				time.Duration(m.Config.RequestTimeout)*time.Second,
			)

			_, success := m.TryLoginCycle(loginCtx)

			cancel()

			if success {
				continue
			}

			fmt.Printf(
				"全部账号尝试失败，%d 秒后再次尝试\n",
				m.Config.RetryInterval,
			)

			select {
			case <-ctx.Done():
				return
			case <-time.After(
				time.Duration(m.Config.RetryInterval) * time.Second,
			):
			}
		}
	}
}

// NormalizeBaseURL 防止用户在配置里写多余的 /。
func NormalizeBaseURL(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}
