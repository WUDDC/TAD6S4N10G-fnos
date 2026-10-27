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

func TestSameHost(t *testing.T) {
	cases := []struct {
		source string
		target string
		want   bool
	}{
		{"192.168.1.10:8123", "192.168.1.10:8123", true},
		{"http://192.168.1.10:8123", "192.168.1.10:8123", true},
		{"192.168.1.10", "192.168.1.10:8123", true}, // 浏览器省略默认端口
		{"192.168.1.10:9999", "192.168.1.10:8123", false},
		{"evil.example", "192.168.1.10:8123", false},
		{"192.168.1.100:8123", "192.168.1.10:8123", false},
	}
	for _, tc := range cases {
		if got := sameHost(tc.source, tc.target); got != tc.want {
			t.Fatalf("sameHost(%q, %q) = %v, want %v", tc.source, tc.target, got, tc.want)
		}
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

func TestSameHostProxyStripsPort(t *testing.T) {
	// 真机复现的形态:DSM nginx 模板 proxy_set_header Host $host 时 $host 不含
	// 端口,浏览器同源 POST 的 Origin 却带端口——必须放行而不是误判跨站
	cases := []struct {
		source string
		target string
		want   bool
	}{
		{"http://10.25.11.2:5000", "10.25.11.2", true}, // 反代剥端口,同主机
		{"https://nas.example.com:5001", "nas.example.com", true},
		{"http://evil.example", "10.25.11.2", false},         // 主机名不同仍拒绝
		{"http://10.25.11.2:9999", "10.25.11.2:5000", false}, // 端口都可见且不同
	}
	for _, tc := range cases {
		if got := sameHost(tc.source, tc.target); got != tc.want {
			t.Fatalf("sameHost(%q, %q) = %v, want %v", tc.source, tc.target, got, tc.want)
		}
	}
}

func TestTCPHandlerAllowsSameHostPortStrippedOrigin(t *testing.T) {
	// 端到端:经剥端口的反代进来,同主机 Origin 的写请求不得被 originGuard 拦
	server := &Server{HTTPAddr: "127.0.0.1:0"}
	handler, err := server.buildTCPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/apply", nil)
	request.Host = "10.25.11.2" // nginx $host:无端口
	request.Header.Set("Origin", "http://10.25.11.2:5000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusForbidden {
		t.Fatalf("同主机 Origin 经剥端口反代被误判跨站: %d", recorder.Code)
	}
}
