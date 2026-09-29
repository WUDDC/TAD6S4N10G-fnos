package powerguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Manager     *Manager
	Socket      string
	WebRoot     string
	BasePath    string
	SocketGroup string
	Logger      *log.Logger
	History     *HistoryStore
}

const configRequestMaxBytes = 3 << 20

func (s *Server) ListenAndServe() error {
	if s.Manager == nil {
		return errors.New("manager is required")
	}
	if s.Socket == "" {
		return errors.New("socket path is required")
	}
	if s.BasePath == "" {
		s.BasePath = "/app/tad-module"
	}
	if s.Logger == nil {
		s.Logger = log.Default()
	}
	if s.SocketGroup == "" {
		s.SocketGroup = "www-data"
	}
	if err := os.MkdirAll(filepath.Dir(s.Socket), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(s.Socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to remove non-socket path %s", s.Socket)
		}
		if err := os.Remove(s.Socket); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", s.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(s.Socket)
	group, err := user.LookupGroup(s.SocketGroup)
	if err != nil {
		return fmt.Errorf("lookup socket group %s: %w", s.SocketGroup, err)
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse socket group %s gid %q: %w", s.SocketGroup, group.Gid, err)
	}
	if err := os.Chown(s.Socket, -1, gid); err != nil {
		return fmt.Errorf("set socket group %s: %w", s.SocketGroup, err)
	}
	if err := os.Chmod(s.Socket, 0o660); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/history/export/sql", s.handleHistoryExportSQL)
	mux.HandleFunc("/api/history/export/csv", s.handleHistoryExportCSV)
	mux.HandleFunc("/api/debug/report", s.handleDebugReport)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/config/global", s.handleGlobalConfig)
	mux.HandleFunc("/api/config/fan", s.handleFanConfig)
	mux.HandleFunc("/api/config/gpio", s.handleGPIOConfig)
	mux.HandleFunc("/api/config/history", s.handleHistoryConfig)
	mux.HandleFunc("/api/config/sensor-names", s.handleSensorNamesConfig)
	mux.HandleFunc("/api/apply", s.handleApply)
	mux.HandleFunc("/api/restore", s.handleRestore)
	mux.Handle("/", http.FileServer(http.Dir(s.WebRoot)))

	handler := s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, s.BasePath) {
			path = strings.TrimPrefix(path, s.BasePath)
			if path == "" {
				path = "/"
			}
			r.URL.Path = path
		}
		mux.ServeHTTP(w, r)
	}))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	return server.Serve(listener)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.History == nil {
		writeJSON(w, http.StatusOK, historyFile{Version: historyFileVersion})
		return
	}
	rangeHours := parseFloatQuery(r, "range", 24)
	samples, interval, err := s.History.Aggregated(r.Context(), rangeHours, historyMaxPoints, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取历史数据失败: "+err.Error())
		return
	}
	defaultGroups := ReclassifySensorGroups(samples)
	ApplySensorGroupOverrides(samples, s.Manager.SensorGroupOverrides())
	writeJSON(w, http.StatusOK, historyFile{Version: historyFileVersion, IntervalSeconds: interval, Samples: samples, DefaultGroups: defaultGroups})
}

// handleHistoryExportSQL 下载 history.db 的一致性快照（VACUUM INTO，含 WAL 数据）。
func (s *Server) handleHistoryExportSQL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.History == nil {
		writeError(w, http.StatusServiceUnavailable, "历史数据存储不可用")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="tad-module-history.db"`)
	if err := s.History.ExportSQLite(r.Context(), w); err != nil {
		s.Logger.Printf("history sql export failed: %v", err)
	}
}

// handleHistoryExportCSV 把全部历史采样导出为宽表 CSV（UTF-8 带 BOM）。
func (s *Server) handleHistoryExportCSV(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.History == nil {
		writeError(w, http.StatusServiceUnavailable, "历史数据存储不可用")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="tad-module-history.csv"`)
	if err := s.History.WriteCSV(r.Context(), w); err != nil {
		s.Logger.Printf("history csv export failed: %v", err)
	}
}

func parseIntQuery(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

func parseFloatQuery(r *http.Request, name string, fallback float64) float64 {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return fallback
	}
	return value
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !isAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可以修改功耗、风扇与按键配置")
		return
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, configRequestMaxBytes))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if err := s.Manager.SaveAndApply(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleGlobalConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeConfigRequest(w, r) {
		return
	}
	var cfg GlobalConfig
	if err := decodeConfigRequest(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if err := s.Manager.SaveGlobalConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleFanConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeConfigRequest(w, r) {
		return
	}
	var cfg FanConfig
	if err := decodeConfigRequest(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if err := s.Manager.SaveFanConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleSensorNamesConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeConfigRequest(w, r) {
		return
	}
	var payload struct {
		Names  map[string]string `json:"names"`
		Groups map[string]string `json:"groups"`
	}
	if err := decodeConfigRequest(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if payload.Names == nil {
		payload.Names = map[string]string{}
	}
	if payload.Groups == nil {
		payload.Groups = map[string]string{}
	}
	if err := s.Manager.SaveSensorSettings(payload.Names, payload.Groups); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleHistoryConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeConfigRequest(w, r) {
		return
	}
	var cfg HistoryConfig
	if err := decodeConfigRequest(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if err := s.Manager.SaveHistoryConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleGPIOConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeConfigRequest(w, r) {
		return
	}
	var cfg GPIOConfig
	if err := decodeConfigRequest(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	if err := s.Manager.SaveGPIOConfig(cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) authorizeConfigRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return false
	}
	if !isAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可以修改模块配置")
		return false
	}
	return true
}

func decodeConfigRequest(r *http.Request, target any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, configRequestMaxBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !isAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可以应用功耗、风扇与按键配置")
		return
	}
	if err := s.Manager.ApplyCurrent(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !isAdmin(r) {
		writeError(w, http.StatusForbidden, "仅管理员可以恢复原始功耗与风扇配置")
		return
	}
	if err := s.Manager.DisableAndRestore(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Manager.Status())
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' https://api.github.com; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

func isAdmin(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Trim-Isadmin")))
	return value == "true" || value == "1" || value == "yes"
}

func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
