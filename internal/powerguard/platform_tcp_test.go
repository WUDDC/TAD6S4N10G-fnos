package powerguard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTreeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectPlatform(t *testing.T) {
	fnOSRoot := t.TempDir()
	writeTreeFile(t, fnOSRoot, "etc/issue", "OS version: fnOS 0.9.22 \\n \\l\n")
	if got := DetectPlatform(fnOSRoot); got != PlatformFnOS {
		t.Fatalf("fnOS via /etc/issue: got %q", got)
	}

	fnOSReleaseRoot := t.TempDir()
	writeTreeFile(t, fnOSReleaseRoot, "etc/fnos-release", "VERSION_ID=\"0.9.22\"\n")
	if got := DetectPlatform(fnOSReleaseRoot); got != PlatformFnOS {
		t.Fatalf("fnOS via /etc/fnos-release: got %q", got)
	}

	dsmRoot := t.TempDir()
	writeTreeFile(t, dsmRoot, "etc/VERSION", "productversion=7.2.2\nbuildnumber=72806\n")
	if got := DetectPlatform(dsmRoot); got != PlatformDSM {
		t.Fatalf("DSM via /etc/VERSION: got %q", got)
	}

	dsmInfoRoot := t.TempDir()
	writeTreeFile(t, dsmInfoRoot, "etc/synoinfo.conf", "unique=synology_apollolake_918+\n")
	if got := DetectPlatform(dsmInfoRoot); got != PlatformDSM {
		t.Fatalf("DSM via synoinfo.conf: got %q", got)
	}

	genericRoot := t.TempDir()
	writeTreeFile(t, genericRoot, "etc/os-release", "PRETTY_NAME=\"Debian GNU/Linux 12\"\n")
	if got := DetectPlatform(genericRoot); got != PlatformGeneric {
		t.Fatalf("generic Debian: got %q", got)
	}

	if got := DetectPlatform(filepath.Join(genericRoot, "nonexistent")); got != PlatformGeneric {
		t.Fatalf("missing root: got %q", got)
	}
}

func TestBindsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8123", true},
		{"127.5.6.7:8123", true},
		{"[::1]:8123", true},
		{"localhost:8123", true},
		{":8123", false},
		{"0.0.0.0:8123", false},
		{"192.168.1.10:8123", false},
		{"[::]:8123", false},
	}
	for _, tc := range cases {
		if got := bindsLoopback(tc.addr); got != tc.want {
			t.Fatalf("bindsLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestLoadTCPCredentials(t *testing.T) {
	dir := t.TempDir()
	server := &Server{AuthFile: filepath.Join(dir, "web-auth.json")}

	if cred, err := server.loadTCPCredentials(); cred != nil || err != nil {
		t.Fatalf("missing file should mean no credentials, got %v, %v", cred, err)
	}

	writeTreeFile(t, dir, "web-auth.json", "{\"username\":\"admin\",\"password\":\"s3cret\"}\n")
	cred, err := server.loadTCPCredentials()
	if err != nil || cred == nil || cred.Username != "admin" || cred.Password != "s3cret" {
		t.Fatalf("valid file parse failed: %v, %+v", err, cred)
	}

	writeTreeFile(t, dir, "web-auth.json", "not json\n")
	if _, err := server.loadTCPCredentials(); err == nil {
		t.Fatal("malformed JSON must fail closed")
	}

	writeTreeFile(t, dir, "web-auth.json", "{\"username\":\"\",\"password\":\"x\"}\n")
	if _, err := server.loadTCPCredentials(); err == nil {
		t.Fatal("empty username must fail closed")
	}
}

func TestBuildTCPHandlerFailsClosedWithoutCredentials(t *testing.T) {
	server := &Server{Manager: &Manager{}}
	if _, err := server.buildTCPHandler(http.NotFoundHandler()); err == nil {
		t.Fatal("non-loopback bind without auth must refuse to start")
	}

	dir := t.TempDir()
	authFile := filepath.Join(dir, "web-auth.json")
	writeTreeFile(t, dir, "web-auth.json", `{"username":"admin","password":"s3cret"}`)
	server.AuthFile = authFile
	if _, err := server.buildTCPHandler(http.NotFoundHandler()); err != nil {
		t.Fatalf("non-loopback bind with valid auth file should start: %v", err)
	}

	loopbackServer := &Server{HTTPAddr: "127.0.0.1:0"}
	if _, err := loopbackServer.buildTCPHandler(http.NotFoundHandler()); err != nil {
		t.Fatalf("loopback bind without auth should start: %v", err)
	}
}

func TestTCPHandlerAuthInjectsAdminIdentity(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "web-auth.json")
	writeTreeFile(t, dir, "web-auth.json", `{"username":"admin","password":"s3cret"}`)
	server := &Server{AuthFile: authFile}

	var sawAdmin bool
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAdmin = IsAdminRequest(r)
	})
	handler, err := server.buildTCPHandler(core)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous request should be 401, got %d", recorder.Code)
	}
	if realm := recorder.Header().Get("WWW-Authenticate"); !strings.Contains(realm, "tad-module") {
		t.Fatalf("401 should carry the realm challenge, got %q", realm)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.SetBasicAuth("admin", "s3cret")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !sawAdmin {
		t.Fatal("successful basic auth must inject the X-Trim-Isadmin identity used by admin gates")
	}

	sawAdmin = false
	request = httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.SetBasicAuth("admin", "wrong")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || sawAdmin {
		t.Fatalf("wrong password should be 401 without admin identity, got %d admin=%v", recorder.Code, sawAdmin)
	}
}

func TestTCPHandlerRejectsCrossOriginWrites(t *testing.T) {
	dir := t.TempDir()
	authFile := filepath.Join(dir, "web-auth.json")
	writeTreeFile(t, dir, "web-auth.json", `{"username":"admin","password":"s3cret"}`)
	server := &Server{AuthFile: authFile}
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}

	authorized := func(request *http.Request) *httptest.ResponseRecorder {
		request.Host = "192.168.1.10:8123"
		request.SetBasicAuth("admin", "s3cret")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	same := authorized(func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Header.Set("Origin", "http://192.168.1.10:8123")
		return request
	}())
	if same.Code != http.StatusOK {
		t.Fatalf("same-origin write should pass, got %d", same.Code)
	}

	cross := authorized(func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Header.Set("Origin", "http://evil.example")
		return request
	}())
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-origin write should be rejected, got %d", cross.Code)
	}

	referer := authorized(func() *http.Request {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Header.Set("Referer", "http://evil.example/page")
		return request
	}())
	if referer.Code != http.StatusForbidden {
		t.Fatalf("cross-origin referer write should be rejected, got %d", referer.Code)
	}

	// 缓存的 Basic 凭据正是跨站 CSRF 的载体,所以跨站写必须拦,即使已认证
	get := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	get.Header.Set("Origin", "http://evil.example")
	get.SetBasicAuth("admin", "s3cret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reads are not CSRF targets, got %d", recorder.Code)
	}

	headless := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	headless.SetBasicAuth("admin", "s3cret")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, headless)
	if recorder.Code != http.StatusOK {
		t.Fatalf("requests without Origin/Referer (curl/脚本) should pass, got %d", recorder.Code)
	}
}

func TestOriginAllowedStrict(t *testing.T) {
	// PR#39 review:null/畸形/非 http(s) scheme/端口不一致一律拒绝;
	// 端口缺失按各自 scheme 默认值归一;scheme 必须与目标侧 proto 一致
	cases := []struct {
		source string
		host   string
		proto  string
		want   bool
	}{
		{"http://10.25.11.2:5000", "10.25.11.2:5000", "http", true},
		{"https://nas.example.com:5001", "nas.example.com:5001", "https", true},
		{"http://nas", "nas", "http", true},             // 双侧默认 80
		{"https://nas", "nas", "https", true},           // 合法 HTTPS 443:Host 无显式端口(review 用例)
		{"https://nas", "nas:443", "https", true},       // 源省略端口=https 默认 443
		{"https://nas:8123", "nas:8123", "http", false}, // 同 host/port 但 scheme 不同(review 用例)
		{"http://nas:8123", "nas:8123", "https", false}, // 反向同理
		{"null", "10.25.11.2:5000", "http", false},      // Origin: null 拒绝
		{"http://evil.example", "10.25.11.2:5000", "http", false},
		{"http://10.25.11.2:9999", "10.25.11.2:5000", "http", false},
		{"http://10.25.11.2:5000", "10.25.11.2", "http", false}, // 5000 vs 默认 80
		{"ftp://10.25.11.2:5000", "10.25.11.2:5000", "http", false},
		{"::not a url::", "10.25.11.2:5000", "http", false},
		{"", "10.25.11.2:5000", "http", false},
	}
	for _, tc := range cases {
		if got := originAllowed(tc.source, tc.host, tc.proto); got != tc.want {
			t.Fatalf("originAllowed(%q, %q, %q) = %v, want %v", tc.source, tc.host, tc.proto, got, tc.want)
		}
	}
}

func TestOriginGuardTrustsForwardedProtoOnlyViaProxyToken(t *testing.T) {
	// X-Forwarded-Proto 只在配置了反代共享密钥(请求必经 nginx)时采信;
	// 直连场景客户端伪造该头必须被忽略
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{Manager: &Manager{}, HTTPAddr: "127.0.0.1:0", ProxyTokenFile: tokenFile}
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	do := func(headers map[string]string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Host = "nas"
		for k, v := range headers {
			request.Header.Set(k, v)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	// 反代 https:Origin https://nas(默认 443) + Host nas → 放行
	if code := do(map[string]string{
		"X-Tad-Proxy-Token": "secret", "X-Forwarded-Proto": "https", "Origin": "https://nas",
	}); code != http.StatusOK {
		t.Fatalf("可信 https 反代同源请求应放行, got %d", code)
	}
	// 反代 https 但 Origin 是 http 源 → 拒绝
	if code := do(map[string]string{
		"X-Tad-Proxy-Token": "secret", "X-Forwarded-Proto": "https", "Origin": "http://nas",
	}); code != http.StatusForbidden {
		t.Fatalf("proto=https 的 http 源应拒绝, got %d", code)
	}

	// 直连(无密钥配置):伪造 X-Forwarded-Proto 无效,http 语义下 https 源拒绝
	direct := &Server{Manager: &Manager{}, HTTPAddr: "127.0.0.1:0"}
	directHandler, err := direct.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.Host = "nas"
	request.Header.Set("X-Forwarded-Proto", "https")
	request.Header.Set("Origin", "https://nas")
	recorder := httptest.NewRecorder()
	directHandler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("直连伪造 proto 的 https 源应拒绝, got %d", recorder.Code)
	}
}

func TestTCPHandlerOriginGuardStrict(t *testing.T) {
	server := &Server{Manager: &Manager{}, HTTPAddr: "127.0.0.1:0"}
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	post := func(origin string) int {
		request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
		request.Host = "10.25.11.2:5000"
		if origin != "__none__" {
			request.Header.Set("Origin", origin)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := post("http://10.25.11.2:5000"); got != http.StatusOK {
		t.Fatalf("同源写应放行, got %d", got)
	}
	if got := post("null"); got != http.StatusForbidden {
		t.Fatalf("Origin: null 应拒绝, got %d", got)
	}
	if got := post("http://10.25.11.2:5001"); got != http.StatusForbidden {
		t.Fatalf("端口不一致应拒绝, got %d", got)
	}
	if got := post("__none__"); got != http.StatusOK {
		t.Fatalf("无 Origin(非浏览器)应放行由鉴权层把关, got %d", got)
	}
}

func TestListenUnixNoneGroupSkipsGroupLookup(t *testing.T) {
	dir := t.TempDir()
	server := &Server{Socket: filepath.Join(dir, "tad.sock"), SocketGroup: "none"}
	listener, err := server.listenUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(server.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket-group=none should create a root-only 0600 socket, got %v", perm)
	}
}

func TestListenUnixRefusesNonSocketPath(t *testing.T) {
	dir := t.TempDir()
	conflict := filepath.Join(dir, "occupied")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{Socket: conflict, SocketGroup: "none"}
	if _, err := server.listenUnix(); err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Fatalf("regular file at socket path must be refused, got %v", err)
	}
}

func TestListenAndServeRequiresListener(t *testing.T) {
	server := &Server{Manager: &Manager{}}
	if err := server.ListenAndServe(); err == nil || !strings.Contains(err.Error(), "socket or http") {
		t.Fatalf("no listener configured should fail fast, got %v", err)
	}
}

func TestAuthFileRoundTripFormat(t *testing.T) {
	// 与群晖 postinst 生成格式保持一致的契约测试:改格式必须同步改安装脚本
	data, err := json.Marshal(basicCredentials{Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	var cred basicCredentials
	if err := json.Unmarshal(data, &cred); err != nil || cred.Username != "admin" || cred.Password != "pw" {
		t.Fatalf("credentials JSON round trip broken: %v %s", err, data)
	}
}

func TestCoreHandlerTrimsCustomBasePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>embedded-ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &Server{Manager: &Manager{}, WebRoot: dir, BasePath: "/webman/3rdparty/tad-module"}
	handler := server.buildCoreHandler()

	// 桌面嵌入模式:DSM nginx 原样透传前缀,handler 整段剥掉后进路由
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/webman/3rdparty/tad-module/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "embedded-ok") {
		t.Fatalf("embedded prefix page broken: %d %s", recorder.Code, recorder.Body.String())
	}

	// 回环直连(无前缀)依然可用
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "embedded-ok") {
		t.Fatalf("root access broken: %d %s", recorder.Code, recorder.Body.String())
	}

	// 前缀下的 API 同样被剥前缀(路由命中,不再 404 于 FileServer)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/webman/3rdparty/tad-module/api/status", nil))
	if recorder.Code == http.StatusNotFound {
		t.Fatal("/api/status under embedded prefix should reach the API mux, not the file server 404")
	}
}

func TestTCPHandlerRejectsPortStrippedHost(t *testing.T) {
	// PR#39 review:剥掉端口的 Host(如 proxy_set_header Host \$host)属于
	// 不受信反代配置——端口对不上就是拒绝,不再放宽
	server := &Server{HTTPAddr: "127.0.0.1:0"}
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.Host = "10.25.11.2" // 剥了端口的 Host
	request.Header.Set("Origin", "http://10.25.11.2:5000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("剥端口 Host + 带端口 Origin 应拒绝, got %d", recorder.Code)
	}
}
