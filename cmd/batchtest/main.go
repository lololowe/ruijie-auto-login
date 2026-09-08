// Command batchtest 是一个独立的锐捷校园网账号批量可用性测试工具。
//
// 用法（在项目根目录，即 test.json 所在目录执行）：
//
//	go run ./cmd/batchtest
//
// 或先编译再运行：
//
//	go build -o batchtest.exe ./cmd/batchtest
//	./batchtest.exe
//
// 读取当前目录下的 test.json，按顺序逐个测试账号；
// 每发现一个可用账号立即写入 available_accounts.json，并从 test.json 中移除。
//
// Options:
//
//	-maxfails N   连续失败 N 次后停止测试（默认 3；N<=0 表示忽略所有失败）
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	inputFile      = "test.json"
	outputFile     = "available_accounts.json"
	probeURL       = "http://119.29.29.29/"
	requestTimeout = 10 * time.Second

	// 连续失败达到该次数后停止整个程序，避免继续触发验证码/风控。
	maxConsecutiveFails = 3

	// 登录成功后确认上线的重试参数。
	verifyRetries  = 5
	verifyInterval = 500 * time.Millisecond

	// 每个账号之间的固定间隔，等待网关注销生效、降低风控压力。
	accountInterval = 2 * time.Second
)

type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	UserIndex    string `json:"userIndex"`
	Result       string `json:"result"`
	Message      string `json:"message"`
	ValidCodeURL string `json:"validCodeUrl"`
}

type onlineUserResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`
	UserID  string `json:"userId"`
	UserIP  string `json:"userIp"`
}

type logoutResponse struct {
	Result  string `json:"result"`
	Message string `json:"message"`
}

// testOutcome 表示单个账号的测试结果。
type testOutcome struct {
	ok     bool   // 账号是否可用
	reason string // 失败原因（成功时为空）
}

// portalURLPattern 用于从锐捷网关拦截响应中提取 ePortal 登录地址，
// 兼容单引号、双引号及不同赋值写法，[^'"]+ 保证 query 中的 & 不被截断。
var portalURLPattern = regexp.MustCompile(
	`['"](https?://[^'"]+/eportal/index\.jsp\?[^'"]+)['"]`,
)

func main() {
	// 连续失败达到该次数后停止整个程序；<=0 表示忽略所有失败，永不停止。
	maxFails := flag.Int(
		"maxfails",
		maxConsecutiveFails,
		"连续失败多少次后停止测试（<=0 表示忽略所有失败）",
	)
	flag.Parse()

	accounts, err := loadAccounts(inputFile)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		os.Exit(1)
	}

	total := len(accounts)
	if *maxFails <= 0 {
		fmt.Println("已启用忽略失败模式：即使连续失败也不会停止")
	}
	fmt.Printf("共加载 %d 个账号，开始批量测试...\n\n", total)

	var (
		available        []Account
		remaining        = append([]Account(nil), accounts...) // 保留在 test.json 中的账号
		successCount     int
		failCount        int
		consecutiveFails int
		stoppedEarly     bool
		sawCaptcha       bool
	)

	for i, acc := range accounts {
		fmt.Printf("正在测试 %d/%d：%s\n", i+1, total, acc.Username)

		outcome, updated := testAccount(acc, available)
		available = updated

		if outcome.ok {
			successCount++
			consecutiveFails = 0

			// 账号可用：从 test.json 中移除并立即写回。
			remaining = removeAccount(remaining, acc)
			if err := writeAccounts(inputFile, remaining); err != nil {
				fmt.Printf("  警告: 更新 %s 失败: %v\n", inputFile, err)
			} else {
				fmt.Printf("  已从 %s 中移除该账号\n", inputFile)
			}
		} else {
			failCount++
			consecutiveFails++
			fmt.Printf("  结果: 不可用（%s）\n", outcome.reason)

			if hasCaptchaSign(outcome.reason) {
				sawCaptcha = true
			}

			// 连续失败过多时立即停止，避免继续触发验证码/风控。
			// maxFails <= 0 时该保护被关闭，忽略所有失败继续测试。
			if *maxFails > 0 && consecutiveFails >= *maxFails {
				stoppedEarly = true
				fmt.Println()
				fmt.Printf("已连续失败 %d 次，停止测试。\n", consecutiveFails)
				if sawCaptcha {
					fmt.Println("已检测到验证码/风控迹象，请手动处理验证码或风控后再测试。")
				} else {
					fmt.Println("可能触发了验证码/风控，或当前网络无法访问校园网认证网关。")
					fmt.Println("请手动检查验证码/风控状态后再继续。")
				}
				break
			}
		}

		fmt.Println()

		// 非最后一个账号时等待片刻，让网关注销状态生效。
		if i < total-1 {
			time.Sleep(accountInterval)
		}
	}

	fmt.Println()
	fmt.Println("========== 测试统计 ==========")
	fmt.Printf("总账号数: %d\n", total)
	fmt.Printf("成功数量: %d\n", successCount)
	fmt.Printf("失败数量: %d\n", failCount)
	fmt.Printf("%s 剩余账号: %d\n", inputFile, len(remaining))
	if stoppedEarly {
		fmt.Printf("因连续失败 %d 次提前停止，剩余 %d 个账号未测试\n",
			consecutiveFails, total-successCount-failCount)
	} else if *maxFails <= 0 {
		fmt.Println("忽略失败模式已开启，已测试全部账号")
	} else {
		fmt.Println("未提前停止，全部账号测试完成")
	}
	if successCount > 0 {
		fmt.Printf("可用账号已保存到 %s\n", outputFile)
	}
}

// loadAccounts 读取并解析账号列表文件。
func loadAccounts(path string) ([]Account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	var accounts []Account
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	if len(accounts) == 0 {
		return nil, fmt.Errorf("%s 中没有账号", path)
	}

	return accounts, nil
}

// testAccount 测试单个账号：
// 重新获取动态参数 → 登录 → 确认上线 → 立即写入结果文件 → 注销。
func testAccount(acc Account, available []Account) (testOutcome, []Account) {
	// 每个账号使用独立的 Client 与 Cookie，避免会话互相影响。
	client := newHTTPClient()

	// 每个账号都重新获取一次动态 QueryString，绝不复用。
	portalBase, queryString, err := discoverPortal(client)
	if err != nil {
		return testOutcome{reason: err.Error()}, available
	}
	fmt.Println("  已获取本次动态登录参数")

	loginResp, err := login(client, portalBase, acc, queryString)
	if err != nil {
		return testOutcome{reason: err.Error()}, available
	}

	if loginResp.Result != "success" {
		reason := "锐捷返回登录失败: " + loginResp.Message
		// 响应中带验证码地址，说明可能需要人工处理验证码。
		if loginResp.ValidCodeURL != "" {
			reason += "（响应包含验证码地址 validCodeUrl）"
		}
		return testOutcome{reason: reason}, available
	}

	fmt.Println("  登录接口返回 success，正在确认是否真正上线...")

	userIndex, online := verifyOnline(client, portalBase, loginResp.UserIndex)
	if !online {
		// 登录接口返回成功但未确认上线：仍尝试注销，避免账号挂在线上。
		fmt.Println("  未能确认上线，仍尝试注销...")
		if userIndex != "" {
			_ = logout(client, portalBase, userIndex)
		}
		return testOutcome{reason: "登录接口返回成功，但未确认账号上线"}, available
	}

	fmt.Println("  已确认账号上线，账号可用")

	// 发现可用账号后立即写入文件，防止程序中途停止丢失已有结果。
	available = append(available, acc)
	if err := writeAccounts(outputFile, available); err != nil {
		fmt.Printf("  警告: 写入 %s 失败: %v\n", outputFile, err)
	} else {
		fmt.Printf("  已写入 %s\n", outputFile)
	}

	// 登录成功后立即注销，再测试下一个账号。
	if userIndex == "" {
		fmt.Println("  警告: 未获取到 userIndex，跳过注销")
	} else if err := logout(client, portalBase, userIndex); err != nil {
		fmt.Printf("  注销失败: %v\n", err)
	} else {
		fmt.Println("  注销成功")
	}

	return testOutcome{ok: true}, available
}

// newHTTPClient 每个账号使用独立的 Client，带 Cookie Jar 与请求超时。
func newHTTPClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{
		Jar:     jar,
		Timeout: requestTimeout,
	}
}

// discoverPortal 请求探测地址，从锐捷网关拦截响应中提取 ePortal 登录 URL，
// 返回 Portal 基础地址（如 http://172.16.32.240）和本次登录用的动态 queryString。
func discoverPortal(client *http.Client) (string, string, error) {
	resp, err := client.Get(probeURL)
	if err != nil {
		return "", "", fmt.Errorf("请求探测地址失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", "", fmt.Errorf("读取探测响应失败: %w", err)
	}

	match := portalURLPattern.FindStringSubmatch(string(body))
	if len(match) < 2 {
		return "", "", errors.New("未找到锐捷 ePortal 跳转地址（可能不在校园网环境）")
	}

	// URL 中的 & 等字符可能被 HTML 实体编码（如 &amp;），需要先还原。
	parsed, err := url.Parse(html.UnescapeString(match[1]))
	if err != nil {
		return "", "", fmt.Errorf("解析 ePortal 地址失败: %w", err)
	}

	if parsed.RawQuery == "" {
		return "", "", errors.New("ePortal 地址中没有 query 参数")
	}

	base := parsed.Scheme + "://" + parsed.Host

	// 与浏览器提交方式一致：先对整个 RawQuery 编码一次，
	// 之后表单 Encode 还会再编码一次。
	queryString := url.QueryEscape(parsed.RawQuery)

	return base, queryString, nil
}

// login 调用锐捷登录接口，参数使用 application/x-www-form-urlencoded 并正确 URL Encode。
func login(client *http.Client, portalBase string, acc Account, queryString string) (*loginResponse, error) {
	target := portalBase + "/eportal/InterFace.do?method=login"

	form := url.Values{}
	form.Set("userId", acc.Username)
	form.Set("password", acc.Password)
	form.Set("service", "")
	form.Set("queryString", queryString)
	form.Set("operatorPwd", "")
	form.Set("operatorUserId", "")
	form.Set("validcode", "")
	form.Set("passwordEncrypt", "false")

	resp, err := client.PostForm(target, form)
	if err != nil {
		return nil, fmt.Errorf("请求登录接口失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("读取登录响应失败: %w", err)
	}

	var result loginResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析登录响应失败: %s", truncate(string(body), 200))
	}

	return &result, nil
}

// verifyOnline 登录成功后确认账号确实上线，
// 返回可用于注销的 userIndex 和是否确认在线。
func verifyOnline(client *http.Client, portalBase string, fallbackUserIndex string) (string, bool) {
	userIndex := fallbackUserIndex

	for i := 0; i < verifyRetries; i++ {
		time.Sleep(verifyInterval)

		// 给 Portal 一点时间同步在线用户信息。
		if idx, err := sessionUserIndex(client, portalBase); err == nil && idx != "" {
			userIndex = idx
		}

		if userIndex == "" {
			continue
		}

		info, err := onlineUserInfo(client, portalBase, userIndex)
		if err != nil {
			continue
		}

		// 锐捷有时 result=wait 但用户信息已经就绪，
		// 因此以 userId + userIp 作为实际在线判断依据。
		if info.UserID != "" && info.UserIP != "" {
			return userIndex, true
		}
	}

	return userIndex, false
}

// sessionUserIndex 通过 redirectortosuccess.jsp 的 302 Location 获取当前会话 userIndex。
func sessionUserIndex(client *http.Client, portalBase string) (string, error) {
	target := portalBase + "/eportal/redirectortosuccess.jsp"

	// 禁止自动跟随 302，需要读取 Location。
	noRedirect := *client
	noRedirect.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	resp, err := noRedirect.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		return "", errors.New("没有登录成功跳转地址")
	}

	parsed, err := url.Parse(location)
	if err != nil {
		return "", err
	}

	return parsed.Query().Get("userIndex"), nil
}

// onlineUserInfo 查询指定 userIndex 的在线用户信息。
func onlineUserInfo(client *http.Client, portalBase, userIndex string) (*onlineUserResponse, error) {
	target := portalBase + "/eportal/InterFace.do?method=getOnlineUserInfo"

	form := url.Values{}
	form.Set("userIndex", userIndex)

	resp, err := client.PostForm(target, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	var result onlineUserResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// logout 调用锐捷注销接口。
func logout(client *http.Client, portalBase, userIndex string) error {
	target := portalBase + "/eportal/InterFace.do?method=logout"

	form := url.Values{}
	form.Set("userIndex", userIndex)

	resp, err := client.PostForm(target, form)
	if err != nil {
		return fmt.Errorf("发送注销请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	if err != nil {
		return fmt.Errorf("读取注销响应失败: %w", err)
	}

	var result logoutResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("解析注销响应失败: %s", truncate(string(body), 200))
	}

	if result.Result != "success" {
		return fmt.Errorf("锐捷返回注销失败: %s", result.Message)
	}

	return nil
}

// writeAccounts 将账号列表整体写入指定文件（每次覆盖写入），
// 避免程序中途停止导致之前的写入结果丢失。
func writeAccounts(path string, accounts []Account) error {
	data, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// removeAccount 从列表中移除与指定账号匹配的条目（用户名+密码均相同才算匹配），
// 返回新列表，未匹配时原样返回。
func removeAccount(list []Account, target Account) []Account {
	result := make([]Account, 0, len(list))
	for _, a := range list {
		if a.Username == target.Username && a.Password == target.Password {
			continue
		}
		result = append(result, a)
	}
	return result
}

// hasCaptchaSign 判断失败信息中是否有验证码/风控迹象。
func hasCaptchaSign(msg string) bool {
	lower := strings.ToLower(msg)
	for _, kw := range []string{"验证码", "validcode", "captcha", "风控"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// truncate 截断过长的响应文本，避免刷屏。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
