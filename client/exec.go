// 本地命令执行接口：让 ChirpBench 参数对比功能可以自己启停被测服务。
//
// 安全边界：
//   - 只监听 127.0.0.1（由 main.go 决定），外部网络访问不到。
//   - 只接受带自定义头 X-ChirpBench-Exec: 1 的 POST。浏览器跨域请求若带自定义头
//     必须先过 CORS 预检，我们不返回 Access-Control-Allow-*，所以恶意网页发不出这个请求。
//   - Origin 必须是 http://127.0.0.1:<本端口> 或 http://localhost:<本端口>（curl 无 Origin 时放行）。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type execReq struct {
	Cmd  string `json:"cmd"`
	Mode string `json:"mode"` // start=后台常驻（docker run / llama-server），run=等它跑完
	Cwd  string `json:"cwd"`
}

var (
	execLogDir string
	execMu     sync.Mutex
	execProcs  = map[int]*os.Process{}
	execExited = map[int]int{} // pid -> exit code（进程已结束时才有）
)

func initExec(port int) {
	dir := filepath.Join(os.TempDir(), "chirpbench-exec")
	_ = os.MkdirAll(dir, 0o755)
	execLogDir = dir
	_ = port
}

func shellCommand(cmd string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/C", cmd)
	}
	return exec.Command("bash", "-lc", cmd)
}

func execGuard(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if r.Header.Get("X-ChirpBench-Exec") != "1" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		host := r.Host
		if !strings.Contains(o, "127.0.0.1") && !strings.Contains(o, "localhost") {
			http.Error(w, "bad origin", http.StatusForbidden)
			return false
		}
		if !strings.Contains(o, host) {
			http.Error(w, "origin mismatch", http.StatusForbidden)
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// POST /__exec  {cmd, mode:"start"|"run", cwd}
func execHandler(w http.ResponseWriter, r *http.Request) {
	if !execGuard(w, r) {
		return
	}
	var req execReq
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	cmd := strings.TrimSpace(req.Cmd)
	if cmd == "" {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "命令为空"})
		return
	}

	tag := make([]byte, 4)
	_, _ = rand.Read(tag)
	logPath := filepath.Join(execLogDir, fmt.Sprintf("%s-%s.log", time.Now().Format("20060102-150405"), hex.EncodeToString(tag)))
	lf, err := os.Create(logPath)
	if err != nil {
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	c := shellCommand(cmd)
	if strings.TrimSpace(req.Cwd) != "" {
		c.Dir = strings.TrimSpace(req.Cwd)
	}
	c.Stdout = lf
	c.Stderr = lf
	detach(c)

	if err := c.Start(); err != nil {
		_ = lf.Close()
		writeJSON(w, 500, map[string]any{"ok": false, "error": err.Error(), "log": logPath})
		return
	}

	if req.Mode == "run" {
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case werr := <-done:
			_ = lf.Close()
			code := 0
			if werr != nil {
				if ee, ok := werr.(*exec.ExitError); ok {
					code = ee.ExitCode()
				} else {
					code = -1
				}
			}
			writeJSON(w, 200, map[string]any{"ok": code == 0, "code": code, "log": logPath, "tail": tailFile(logPath, 4000)})
		case <-time.After(10 * time.Minute):
			writeJSON(w, 200, map[string]any{"ok": false, "error": "命令超时(10分钟)", "log": logPath})
		}
		return
	}

	// start 模式：立刻返回，进程在后台继续；后台回收避免僵尸
	pid := c.Process.Pid
	execMu.Lock()
	execProcs[pid] = c.Process
	execMu.Unlock()
	go func() {
		werr := c.Wait()
		_ = lf.Close()
		code := 0
		if werr != nil {
			if ee, ok := werr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = -1
			}
		}
		execMu.Lock()
		delete(execProcs, pid)
		execExited[pid] = code
		execMu.Unlock()
	}()

	writeJSON(w, 200, map[string]any{"ok": true, "pid": pid, "log": logPath})
}

// GET /__exec/log?path=...  取命令输出末尾，用于启动失败时展示原因
func execLogHandler(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" || !strings.Contains(p, "chirpbench-exec") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, tailFile(p, 8000))
}

func tailFile(path string, n int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	off := int64(0)
	if size > n {
		off = size - n
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return ""
	}
	return string(buf)
}

// GET /__exec/status?pid=N  看启动命令是还活着还是已经退出了（退出码可以判断是秒退还是正常）
func execStatusHandler(w http.ResponseWriter, r *http.Request) {
	pid := 0
	fmt.Sscanf(r.URL.Query().Get("pid"), "%d", &pid)
	if pid <= 0 {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad pid"})
		return
	}
	execMu.Lock()
	_, alive := execProcs[pid]
	code, exited := execExited[pid]
	execMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "running": alive, "exited": exited, "code": code})
}

// POST /__kill  {pid}  兜底：停掉某个由本程序启动的进程
func killHandler(w http.ResponseWriter, r *http.Request) {
	if !execGuard(w, r) {
		return
	}
	var req struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "bad json"})
		return
	}
	execMu.Lock()
	p := execProcs[req.PID]
	execMu.Unlock()
	if p == nil {
		writeJSON(w, 404, map[string]any{"ok": false, "error": "没有这个进程"})
		return
	}
	_ = p.Kill()
	writeJSON(w, 200, map[string]any{"ok": true})
}
