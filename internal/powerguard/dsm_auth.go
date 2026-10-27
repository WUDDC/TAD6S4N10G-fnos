package powerguard

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DSM 会话鉴权（桌面嵌入模式）。PR#39 review 结论：DSM 的 /webman/3rdparty/
// 路径本身不做登录校验（第三方 CGI 都要自己调 authenticate.cgi），反代不能
// 无条件注入管理员身份——否则任何能到达 5000/5001 端口的人（含未登录者与
// QuickConnect/端口映射场景下的公网）都会被当作管理员。
//
// 校验机制与 SPM 的 CGI 同源：本地执行 DSM 自带的
// /usr/syno/synoman/webman/modules/authenticate.cgi，把请求的完整 Cookie 头
// 经 HTTP_COOKIE 环境变量传入；有效会话时它以退出码 0 输出用户名，无效/
// 缺失时非零退出且无输出（真机实测：无 cookie exit=5，伪/空 id exit=4）。
// 拿到用户名后还必须落在 administrators 组（/etc/group）才放行为管理员。

// DSMAuthenticateBinPath DSM 会话校验可执行文件的绝对路径(供 main 组装)。
func DSMAuthenticateBinPath() string { return dsmAuthDefaultBin }

const (
	dsmAuthDefaultBin      = "/usr/syno/synoman/webman/modules/authenticate.cgi"
	dsmAuthDefaultSessions = "/usr/syno/etc/private/session/current.users"
	dsmAuthDefaultGroup    = "/etc/group"
	dsmAuthAdminGroup      = "administrators"
	dsmAuthCacheTTL        = 30 * time.Second
	dsmAuthExecuteTimeout  = 5 * time.Second
	dsmAuthSessionCookieID = "id"
)

// DSMSessionAuth 校验请求携带的 DSM 会话并要求管理员身份。
// 正向结果按会话 id 短暂缓存（面板每 5 秒轮询，避免每个请求都起进程）；
// 拒绝不缓存——伪会话 id 无限多，缓存反而给攻击者刷内存的口子。
type DSMSessionAuth struct {
	// AuthenticateBin 会话校验可执行文件（测试可注入替身脚本）
	AuthenticateBin string
	// GroupFile 管理员组来源（/etc/group；测试可指向夹具）
	GroupFile string
	// SessionFile DSM 7 会话注册表(JSON-lines: name/id/connTime/duration)。
	// 真机(DSM 7.3.2/RR)实测 authenticate.cgi 对有效 webui 会话也拒绝
	// (SPM 的 CGI 同样失效),会话注册表是本机唯一可靠的校验源;root-only。
	// 为空时只用 authenticate.cgi(旧 DSM)。
	SessionFile string
	// AdminGroup 要求的组名，空则用 administrators
	AdminGroup string

	mu      sync.Mutex
	entries map[string]dsmAuthEntry
}

type dsmAuthEntry struct {
	user    string
	allowed bool // 已登录且属 administrators 组才为 true;负缓存必须可判
	expires time.Time
}

// NewDSMSessionAuth 按真实 DSM 路径构造。
func NewDSMSessionAuth() *DSMSessionAuth {
	return &DSMSessionAuth{
		AuthenticateBin: dsmAuthDefaultBin,
		GroupFile:       dsmAuthDefaultGroup,
		SessionFile:     dsmAuthDefaultSessions,
		AdminGroup:      dsmAuthAdminGroup,
		entries:         map[string]dsmAuthEntry{},
	}
}

// dsmSessionID 从 Cookie 头提取 DSM 会话 id 的值。
func dsmSessionID(cookieHeader string) string {
	for _, part := range strings.Split(cookieHeader, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(name) == dsmAuthSessionCookieID {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// authorize 校验会话并要求 administrators 组成员。cookieHeader 是请求的
// 完整 Cookie 头原样转发给 authenticate.cgi。
func (a *DSMSessionAuth) authorize(cookieHeader string) (bool, string) {
	sid := dsmSessionID(cookieHeader)
	if sid == "" {
		return false, ""
	}
	now := time.Now()
	a.mu.Lock()
	if entry, ok := a.entries[sid]; ok {
		if now.Before(entry.expires) {
			a.mu.Unlock()
			return entry.allowed, entry.user
		}
		delete(a.entries, sid)
	}
	a.mu.Unlock()

	// 优先 DSM 7 会话注册表(本机文件,root-only):DSM 7.3/RR 上
	// authenticate.cgi 已无法校验 webui 会话(SPM 的 CGI 同样失效)。
	// 注册表只证明"已登录",管理员身份仍须过组校验
	if user, ok := a.authorizeViaSessionRegistry(sid, time.Now()); ok {
		if a.userIsAdmin(user) {
			a.remember(sid, user, time.Now())
			return true, user
		}
		a.remember(sid, "", time.Now())
		return false, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), dsmAuthExecuteTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, a.AuthenticateBin)
	command.Env = append(os.Environ(), "HTTP_COOKIE="+cookieHeader)
	output, err := command.Output()
	if err != nil {
		// 非零退出码 = DSM 判定会话无效（或 cgi 不可用，一律拒绝）
		return false, ""
	}
	user := strings.TrimSpace(string(output))
	if user == "" || strings.ContainsAny(user, " \t\r\n") {
		return false, ""
	}
	if !a.userIsAdmin(user) {
		// 已登录但不是管理员：不给管理员身份。正向缓存也记下这一判定,
		// 免得同一个低权限会话反复起进程
		a.remember(sid, "", now)
		return false, ""
	}
	a.remember(sid, user, now)
	return true, user
}

// authorizeViaSessionRegistry 按 id cookie 值查 DSM 会话注册表。注册表是
// JSON-lines:name(用户名)/id(会话令牌,即浏览器 HttpOnly 的 id cookie 值)/
// connTime(秒)/duration(分钟;0 与 -1 为不限时,>0 时校验过期)。
// 注销时 DSM 会移除条目;id 是 86 字符随机串,命中即视为持有有效会话。
func (a *DSMSessionAuth) authorizeViaSessionRegistry(sid string, now time.Time) (string, bool) {
	if a.SessionFile == "" {
		return "", false
	}
	data, err := os.ReadFile(a.SessionFile)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, `"id"`) {
			continue
		}
		var entry struct {
			Name     string `json:"name"`
			ID       string `json:"id"`
			ConnTime string `json:"connTime"`
			Duration string `json:"duration"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry.ID == "" || entry.ID != sid || entry.Name == "" {
			continue
		}
		// fail-closed(PR#39 review 三轮):注册表是本路径唯一的授权依据,
		// 任何字段缺失/畸形/越界/未知取值都不得默认放行——该行按不匹配
		// 处理,继续找下一条(全不匹配则落到 cgi 兜底,兜底失败即拒绝)。
		// 字段语义依据 DSM 7.3.2 真机注册表实测:connTime 为登录时刻的
		// 秒级 epoch;duration 为分钟数,webui 即时会话=0、SYNO.CCC 服务
		// 会话=-1(均不限时,注销即删行),stay-login=正数(如 10080);
		// 除 0/-1 外的负值从未出现,一律按未知取值拒绝。
		if strings.ContainsAny(entry.Name, " \t\r\n") {
			continue
		}
		connTime, err := strconv.ParseInt(entry.ConnTime, 10, 64)
		if err != nil || connTime <= 0 {
			continue
		}
		duration, err := strconv.ParseInt(entry.Duration, 10, 64)
		if err != nil {
			continue
		}
		switch {
		case duration == 0 || duration == -1:
			// 不限时哨兵:仅这两个已观测语义放行
		case duration > 0:
			// 有界会话:过期加法先防溢出,溢出按未知取值拒绝
			if duration > (math.MaxInt64-connTime)/60 {
				continue
			}
			if now.Unix() >= connTime+duration*60 {
				continue // 已过期
			}
		default:
			continue // 未知负值
		}
		return entry.Name, true
	}
	return "", false
}

func (a *DSMSessionAuth) remember(sid, user string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.entries == nil {
		a.entries = map[string]dsmAuthEntry{}
	}
	a.entries[sid] = dsmAuthEntry{user: user, allowed: user != "", expires: now.Add(dsmAuthCacheTTL)}
}

// userIsAdmin 解析 /etc/group 检查用户是否在 administrators 组的成员列表里。
func (a *DSMSessionAuth) userIsAdmin(user string) bool {
	group := a.AdminGroup
	if group == "" {
		group = dsmAuthAdminGroup
	}
	data, err := os.ReadFile(a.GroupFile)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 || fields[0] != group {
			continue
		}
		for _, member := range strings.Split(fields[3], ",") {
			if strings.TrimSpace(member) == user {
				return true
			}
		}
	}
	return false
}

// Handler HTTP 中间件：通过 DSM 会话+管理员组校验的请求注入与 fnOS 网关
// 相同的管理员身份头，其余一律 401。
func (a *DSMSessionAuth) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, _ := a.authorize(r.Header.Get("Cookie")); ok {
			r.Header.Set("X-Trim-Isadmin", "true")
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "需要 DSM 管理员登录")
	})
}
