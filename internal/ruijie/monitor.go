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
			"\n[%s] 正在尝试账号 [%d/%d]: %s\n",
			ts(),
			m.CurrentIndex+1,
			accountCount,
			account.Username,
		)

		user, err := m.Client.LoginAndVerify(ctx, account)

		if err == nil && user != nil {
			fmt.Printf("[%s] 登录成功\n", ts())
			fmt.Printf("[%s] 账号: %s\n", ts(), user.UserID)
			fmt.Printf("[%s] IP: %s\n", ts(), user.UserIP)

			m.MatchCurrentUser(user.UserID)

			return user, true
		}

		if err != nil {
			fmt.Printf("[%s] 登录失败: %v\n", ts(), err)
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

// offlineConfirmThreshold 是确认掉线所需的连续 OFFLINE 次数。
// 单次 OFFLINE 可能只是 Portal 侧的瞬时抖动，必须连续确认后才触发账号轮换。
const offlineConfirmThreshold = 3

// ts 返回日志用的时间戳。
func ts() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// internetText 将互联网检测结果转为可读文本。
func internetText(ok bool) string {
	if ok {
		return "正常"
	}
	return "不可用"
}

// prevStateLabel 返回状态变化横幅中“变化前”状态的标签。
// -1 表示尚未进行过检测（首轮）。
func prevStateLabel(s PortalState) string {
	if int(s) < 0 {
		return "启动"
	}
	return s.String()
}

// Monitor 持续监测网络与登录状态。
//
// 状态机规则：
//   - ONLINE:  清零掉线计数；互联网检测失败只记录，不重新登录
//   - UNKNOWN: 查询失败/超时，不累计为 OFFLINE，也不重新登录
//   - OFFLINE: 累计确认，连续达到阈值才确认掉线并轮换账号重新登录
//
// 日志规则：
//   - Portal 状态发生变化时打印详细横幅（时间戳、变化前后状态、账号、原因、处理动作）
//   - ONLINE 期间互联网状态单独跟踪，变化时也打印横幅（但绝不触发重新登录）
//   - 状态无变化时只打印单行心跳日志
func (m *Manager) Monitor(ctx context.Context) {
	ticker := time.NewTicker(
		time.Duration(m.Config.CheckInterval) * time.Second,
	)
	defer ticker.Stop()

	fmt.Printf(
		"\n开始监控，检测间隔: %d 秒（连续 %d 次 OFFLINE 才确认掉线）\n",
		m.Config.CheckInterval,
		offlineConfirmThreshold,
	)

	var (
		offlineCount int               // 连续 OFFLINE 计数
		unknownCount int               // 连续 UNKNOWN 计数（仅用于日志提示）
		lastState    = PortalState(-1) // 上一轮 Portal 状态，-1 表示尚未检测
		lastInternet *bool             // 上一轮互联网状态，nil 表示尚未记录
		lastAccount  string            // 最后一次确认 ONLINE 的账号
		lastIP       string            // 最后一次确认 ONLINE 的 IP
	)

	timeout := time.Duration(m.Config.RequestTimeout) * time.Second

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			/*
				Portal 状态查询：GetCurrentUser 内部会为“会话查询”和
				“用户信息查询”分别创建独立的超时上下文。
				这里直接传入长生命周期的 ctx，不再额外包一层超时，
				确保每个请求都能拿到完整的超时预算，
				也避免与后面的互联网检测共享同一个上下文。
			*/
			user, state, queryErr := m.Client.GetCurrentUser(ctx)

			/*
				互联网连通性检测使用独立超时上下文。
				它只反映互联网可达性，不参与登录状态判断，
				临时失败绝不能触发重新登录。
			*/
			internetCtx, internetCancel := context.WithTimeout(ctx, timeout)
			internetOK := m.Client.CheckInternet(internetCtx)
			internetCancel()

			stateChanged := state != lastState

			switch state {
			case PortalOnline:
				prevOffline := offlineCount

				// 恢复在线，立即清零各类计数。
				offlineCount = 0
				unknownCount = 0

				m.MatchCurrentUser(user.UserID)
				lastAccount, lastIP = user.UserID, user.UserIP

				if stateChanged {
					fmt.Println()
					fmt.Printf(
						"[%s] ========== 状态变化: %s → ONLINE ==========\n",
						ts(),
						prevStateLabel(lastState),
					)

					if prevOffline > 0 {
						fmt.Printf(
							"[%s] 掉线计数清零（此前连续确认: %d/%d）\n",
							ts(),
							prevOffline,
							offlineConfirmThreshold,
						)
					}

					fmt.Printf(
						"[%s] 账号: %s | 用户名: %s | IP: %s\n",
						ts(),
						user.UserID,
						user.UserName,
						user.UserIP,
					)
					fmt.Printf(
						"[%s] 互联网状态: %s\n",
						ts(),
						internetText(internetOK),
					)

					if !internetOK {
						/*
							Portal 在线但互联网检测失败：
							可能只是检测站点临时不可达，
							记录异常即可，绝不因此重新登录或轮换账号。
						*/
						fmt.Printf(
							"[%s] 注意：Portal 会话有效，互联网检测失败不触发重新登录\n",
							ts(),
						)
					}

					fmt.Println("==================================================")
				} else if lastInternet != nil && *lastInternet != internetOK {
					// ONLINE 保持不变，仅互联网状态发生翻转。
					fmt.Println()
					fmt.Printf(
						"[%s] ========== 互联网状态变化: %s → %s ==========\n",
						ts(),
						internetText(*lastInternet),
						internetText(internetOK),
					)
					fmt.Printf(
						"[%s] 当前状态: ONLINE | 账号: %s | IP: %s\n",
						ts(),
						user.UserID,
						user.UserIP,
					)
					fmt.Printf(
						"[%s] 注意：Portal 会话仍然有效，不重新登录\n",
						ts(),
					)
					fmt.Println("==================================================")
				} else {
					fmt.Printf(
						"[%s] 心跳: ONLINE | 账号: %s | IP: %s | 互联网: %s\n",
						ts(),
						user.UserID,
						user.UserIP,
						internetText(internetOK),
					)
				}

			case PortalUnknown:
				unknownCount++

				if stateChanged {
					fmt.Println()
					fmt.Printf(
						"[%s] ========== 状态变化: %s → UNKNOWN ==========\n",
						ts(),
						prevStateLabel(lastState),
					)

					if lastAccount != "" {
						fmt.Printf(
							"[%s] 最后确认在线: 账号 %s | IP: %s\n",
							ts(),
							lastAccount,
							lastIP,
						)
					}

					fmt.Printf("[%s] 原因: %v\n", ts(), queryErr)
					fmt.Printf(
						"[%s] 处理: 暂不重新登录，不累计掉线，等待下一轮检测\n",
						ts(),
					)
					fmt.Println("==================================================")
				} else {
					fmt.Printf(
						"[%s] 心跳: UNKNOWN（连续 %d 轮）| 原因: %v\n",
						ts(),
						unknownCount,
						queryErr,
					)
				}

			case PortalOffline:
				offlineCount++
				unknownCount = 0

				if stateChanged {
					fmt.Println()
					fmt.Printf(
						"[%s] ========== 状态变化: %s → OFFLINE ==========\n",
						ts(),
						prevStateLabel(lastState),
					)

					if lastAccount != "" {
						fmt.Printf(
							"[%s] 掉线账号: %s | IP: %s\n",
							ts(),
							lastAccount,
							lastIP,
						)
					}
				}

				if offlineCount < offlineConfirmThreshold {
					fmt.Printf(
						"[%s] 当前状态: OFFLINE | 连续确认: %d/%d | 互联网: %s | 尚未确认掉线，等待下一轮检测\n",
						ts(),
						offlineCount,
						offlineConfirmThreshold,
						internetText(internetOK),
					)
					continue
				}

				// 连续确认达到阈值，正式判定掉线。
				fmt.Println()
				fmt.Printf(
					"[%s] ========== 确认掉线（连续 %d/%d 次 OFFLINE） ==========\n",
					ts(),
					offlineCount,
					offlineConfirmThreshold,
				)

				if lastAccount != "" {
					fmt.Printf(
						"[%s] 失效账号: %s | IP: %s\n",
						ts(),
						lastAccount,
						lastIP,
					)
				}

				fmt.Printf(
					"[%s] 互联网状态: %s\n",
					ts(),
					internetText(internetOK),
				)
				fmt.Printf("[%s] 开始账号轮换并重新登录...\n", ts())
				fmt.Println("==================================================")

				// 确认掉线后清零计数，重新登录后从头开始统计。
				offlineCount = 0

				/*
					当前账号掉线后，直接从下一个账号开始。
					若尚未匹配到任何账号（如启动时状态为 UNKNOWN），
					则交由 TryLoginCycle 走随机起点逻辑。
				*/
				if m.CurrentIndex >= 0 {
					m.NextAccount()
				}

				// 登录流程使用独立的超时上下文。
				loginCtx, loginCancel := context.WithTimeout(ctx, timeout)

				_, success := m.TryLoginCycle(loginCtx)

				loginCancel()

				if success {
					continue
				}

				fmt.Printf(
					"[%s] 全部账号尝试失败，%d 秒后再次尝试\n",
					ts(),
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

			lastState = state
			lastInternet = &internetOK
		}
	}
}

// NormalizeBaseURL 防止用户在配置里写多余的 /。
func NormalizeBaseURL(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), "/")
}
