package powerguard

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDSMAuth 替身:按会话 id 的值决定输出——有效会话输出用户名并以 0 退出,
// 其余输出空并以非 0 退出(镜像真机 authenticate.cgi 的行为契约)。
func fakeDSMAuth(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "authenticate.cgi")
	body := "#!/bin/sh\n" +
		"case \"$HTTP_COOKIE\" in\n" +
		"  *\"id=good-admin\"*) echo admin; exit 0;;\n" +
		"  *\"id=good-user\"*) echo plainuser; exit 0;;\n" +
		"  *) exit 4;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func adminGroupFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "group")
	content := "users:x:100:\nadministrators:x:101:admin,heweicai\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDSMSessionAuthAuthorizesAdminOnly(t *testing.T) {
	bin := fakeDSMAuth(t)
	auth := &DSMSessionAuth{AuthenticateBin: bin, GroupFile: adminGroupFixture(t)}

	if ok, user := auth.authorize(""); ok || user != "" {
		t.Fatal("无 Cookie 必须拒绝")
	}
	if ok, user := auth.authorize("id=bogus; other=1"); ok || user != "" {
		t.Fatal("伪会话必须拒绝")
	}
	if ok, user := auth.authorize("id=good-user"); ok || user != "" {
		t.Fatal("已登录但非 administrators 组不得成为管理员")
	}
	ok, user := auth.authorize("id=good-admin; StayLogin=1")
	if !ok || user != "admin" {
		t.Fatalf("管理员会话应通过: %v %q", ok, user)
	}
}

func TestDSMSessionAuthCachesPositive(t *testing.T) {
	// 有效会话短暂缓存:面板 5 秒轮询不应每个请求都起进程
	bin := fakeDSMAuth(t)
	// 用计数替身:直接包装 script
	dir := filepath.Dir(bin)
	counter := filepath.Join(dir, "count")
	wrapper := filepath.Join(dir, "wrapper.cgi")
	body := "#!/bin/sh\n" +
		"n=$(cat " + counter + ")\n" +
		"echo $((n+1)) > " + counter + "\n" +
		"exec " + bin + "\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(counter, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	auth := &DSMSessionAuth{AuthenticateBin: wrapper, GroupFile: adminGroupFixture(t)}

	for i := 0; i < 3; i++ {
		if ok, _ := auth.authorize("id=good-admin"); !ok {
			t.Fatalf("第 %d 次校验应通过", i+1)
		}
	}
	data, _ := os.ReadFile(counter)
	if got := strings.TrimSpace(string(data)); got != "1" {
		t.Fatalf("正向缓存应只执行一次校验,实际 %s 次", got)
	}
	// 不同会话 id 不共享缓存
	if ok, _ := auth.authorize("id=good-user"); ok {
		t.Fatal("非管理员会话不得放行")
	}
}

func TestTCPHandlerDSMEmbeddedChain(t *testing.T) {
	// PR#39 review 的核心场景:桌面嵌入模式下的完整 TCP 链
	// 剥头→密钥→同源→DSM 会话(管理员组)→业务
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "proxy-token")
	if err := os.WriteFile(tokenFile, []byte("secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	auth := &DSMSessionAuth{AuthenticateBin: fakeDSMAuth(t), GroupFile: adminGroupFixture(t)}
	server := &Server{DSMAuth: auth, ProxyTokenFile: tokenFile}

	var sawAdmin bool
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAdmin = IsAdminRequest(r)
		w.WriteHeader(http.StatusOK)
	})
	handler, err := server.buildTCPHandler(core)
	if err != nil {
		t.Fatal(err)
	}
	do := func(token, cookieHeader, origin string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Host = "10.25.11.2:5000"
		if token != "" {
			request.Header.Set("X-Tad-Proxy-Token", token)
		}
		if cookieHeader != "" {
			request.Header.Set("Cookie", cookieHeader)
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	// 1. 无密钥:拒绝(本机进程也不能绕过)
	if rec := do("", "id=good-admin", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("缺共享密钥应 403, got %d", rec.Code)
	}
	// 2. 有密钥、未登录:401(review 要求的未登录复现)
	if rec := do("secret-token", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d", rec.Code)
	}
	// 3. 有密钥、伪会话:401
	if rec := do("secret-token", "id=bogus", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪会话应 401, got %d", rec.Code)
	}
	// 4. 有密钥、非管理员会话:401
	if rec := do("secret-token", "id=good-user", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("非管理员会话应 401, got %d", rec.Code)
	}
	// 5. 客户端自带 X-Trim-Isadmin 且无有效会话:剥头后 401,不得成为管理员
	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.Host = "10.25.11.2:5000"
	request.Header.Set("X-Tad-Proxy-Token", "secret-token")
	request.Header.Set("X-Trim-Isadmin", "true")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || sawAdmin {
		t.Fatalf("自带头必须被剥掉: %d admin=%v", recorder.Code, sawAdmin)
	}
	// 6. 完整合法链:200 且管理员身份由内部注入
	if rec := do("secret-token", "id=good-admin", "http://10.25.11.2:5000"); rec.Code != http.StatusOK || !sawAdmin {
		t.Fatalf("合法链应 200 且为管理员: %d admin=%v", rec.Code, sawAdmin)
	}
	// 7. 合法会话但跨站 Origin:403
	if rec := do("secret-token", "id=good-admin", "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("跨站写应 403, got %d", rec.Code)
	}
}

func TestTCPHandlerStripsClientAdminHeaderInBasicMode(t *testing.T) {
	// 独立端口模式(Basic)同样剥头:通过认证的请求身份来自内部注入,
	// 未认证的即使自带头也不能进管理处理器
	dir := t.TempDir()
	authFile := filepath.Join(dir, "web-auth.json")
	if err := os.WriteFile(authFile, []byte(`{"username":"admin","password":"s3cret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{AuthFile: authFile}
	var sawAdmin bool
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAdmin = IsAdminRequest(r)
	}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.Header.Set("X-Trim-Isadmin", "true") // 自带头,无凭据
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || sawAdmin {
		t.Fatalf("自带头不得绕过 Basic: %d admin=%v", recorder.Code, sawAdmin)
	}
}

func sessionRegistryFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "current.users")
	content := `{ "host": "10.25.11.1", "name": "heweicai", "id": "goodwebui", "connTime": "1793105496", "duration": "0", "abn": "1", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "expiredbounded", "connTime": "1000", "duration": "60", "app": "finder" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "plainuser", "id": "plain-session", "connTime": "1793105496", "duration": "-1", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "no-duration", "connTime": "1793105496", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "garbage-duration", "connTime": "1793105496", "duration": "abc", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "garbage-conntime", "connTime": "not-a-number", "duration": "0", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "unknown-negative", "connTime": "1793105496", "duration": "-5", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "overflow-duration", "connTime": "1793105496", "duration": "9223372036854775807", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "name": "heweicai", "id": "zero-conntime", "connTime": "0", "duration": "0", "app": "webui" }` + "\n" +
		`{ "host": "10.25.11.2", "id": "missing-name", "connTime": "1793105496", "duration": "0", "app": "webui" }` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDSMSessionAuthViaRegistry(t *testing.T) {
	// DSM 7.3 真机形态:会话注册表(JSON-lines)是唯一可靠校验源
	// (authenticate.cgi 在该环境对有效 webui 会话也拒绝)
	auth := &DSMSessionAuth{
		AuthenticateBin: "/nonexistent/authenticate.cgi",
		GroupFile:       adminGroupFixture(t),
		SessionFile:     sessionRegistryFixture(t),
	}
	ok, user := auth.authorize("id=goodwebui")
	if !ok || user != "heweicai" {
		t.Fatalf("注册表命中应放行: %v %q", ok, user)
	}
	// 有界会话已过期:拒绝
	if ok, _ := auth.authorize("id=expiredbounded"); ok {
		t.Fatal("过期有界会话应拒绝")
	}
	// 会话有效但非 administrators 组:拒绝
	if ok, _ := auth.authorize("id=plain-session"); ok {
		t.Fatal("非管理员会话应拒绝")
	}
	// 未知会话(=已注销,DSM 删行):拒绝
	if ok, _ := auth.authorize("id=nope"); ok {
		t.Fatal("未注册会话应拒绝")
	}
	// fail-closed:字段缺失/畸形/未知取值一律拒绝,不得默认授权
	for _, sid := range []string{"no-duration", "garbage-duration", "garbage-conntime", "unknown-negative", "overflow-duration", "zero-conntime", "missing-name"} {
		if ok, _ := auth.authorize("id=" + sid); ok {
			t.Fatalf("畸形/未知取值会话 %s 必须拒绝", sid)
		}
	}
}

func TestDSMSessionAuthNegativeCacheDoesNotGrant(t *testing.T) {
	// 回归:非管理员会话的负缓存命中时必须仍拒绝(曾因 user="" 命中
	// 缓存被当作 ok=true 放过)
	auth := &DSMSessionAuth{
		AuthenticateBin: "/nonexistent/authenticate.cgi",
		GroupFile:       adminGroupFixture(t),
		SessionFile:     sessionRegistryFixture(t),
	}
	for i := 0; i < 2; i++ {
		if ok, user := auth.authorize("id=plain-session"); ok || user != "" {
			t.Fatalf("第 %d 次(含负缓存命中)非管理员必须拒绝: %v %q", i+1, ok, user)
		}
	}
}

func TestDSMSessionAuthHandlerRejectsNonAdminRepeatedly(t *testing.T) {
	// PR#39 review:非管理员会话在缓存有效期内连续请求,每次都必须 401
	// 且下游管理处理器绝不能被调用(走完整 Handler 链验证)
	auth := &DSMSessionAuth{
		AuthenticateBin: "/nonexistent/authenticate.cgi",
		GroupFile:       adminGroupFixture(t),
		SessionFile:     sessionRegistryFixture(t),
	}
	called := 0
	handler := auth.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	for i := 1; i <= 3; i++ {
		request := httptest.NewRequest(http.MethodPost, "/api/config/global", nil)
		request.Header.Set("Cookie", "id=plain-session")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次请求应为 401, got %d", i, recorder.Code)
		}
	}
	if called != 0 {
		t.Fatalf("非管理员请求不得触达下游处理器, called=%d", called)
	}
}
