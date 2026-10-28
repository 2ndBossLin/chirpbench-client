// ChirpBench 本地测速客户端（单文件代理）
//
// 作用：解决浏览器直连模型的两道墙
//   1) CORS：页面由本程序从 http://127.0.0.1:PORT 提供，页面里的跨域请求被改写成
//      同源请求 /__proxy?u=<原始 URL>，由本程序以普通 HTTP 客户端身份转发（无浏览器参与）。
//   2) 混合内容：页面是 http://127.0.0.1，不是 https，因此调 http://内网IP 不会被拦。
//
// 关键点：流式响应必须逐块 flush，否则 prefill（首字延迟）测不准。
package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

//go:embed web
var webFS embed.FS

const version = "0.1.0"

func main() {
	port := flag.Int("port", 18099, "本地监听端口")
	open := flag.Bool("open", true, "启动后自动打开浏览器")
	flag.Parse()

	addr := fmt.Sprintf("127.0.0.1:%d", *port)

	mux := http.NewServeMux()
	mux.HandleFunc("/__health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":"%s","proxy":true}`, version)
	})
	mux.HandleFunc("/__proxy", proxyHandler)
	mux.HandleFunc("/__hw", hwHandler)
	mux.HandleFunc("/__exec", execHandler)
	mux.HandleFunc("/__exec/log", execLogHandler)
	mux.HandleFunc("/__exec/status", execStatusHandler)
	mux.HandleFunc("/__kill", killHandler)
	mux.HandleFunc("/__info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"proxy":true,"version":"%s"}`, version)
	})
	mux.HandleFunc("/", pageHandler)

	// 端口被占用时自动往后找空位（用户很可能只是双击 exe，看不到命令行提示）
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		base := *port
		for p := base + 1; p <= base+20; p++ {
			if l2, e2 := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); e2 == nil {
				ln, err, *port = l2, nil, p
				addr = fmt.Sprintf("127.0.0.1:%d", p)
				log.Printf("端口 %d 已被占用，自动改用 %d", base, p)
				break
			}
		}
	}
	if err != nil {
		log.Fatalf("端口 %d 及后续 20 个端口都被占用或被拒绝：%v\n可以用 -port 指定其它端口。", *port, err)
	}
	initExec(*port)

	pageURL := "http://" + addr + "/"
	fmt.Printf("ChirpBench 本地客户端 v%s 已启动\n页面地址：%s\n（关闭这个窗口即停止。被测模型可以在这台机器，也可以在同一局域网的其它机器）\n\n", version, pageURL)

	if *open {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openBrowser(pageURL)
		}()
	}

	srv := &http.Server{Handler: noCache(mux)}
	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

// pageHandler 返回内嵌页面，并注入标记脚本，让页面知道自己在代理模式下运行。
func pageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		// 静态资源（图标等）直接从内嵌文件读
		name := strings.TrimPrefix(r.URL.Path, "/")
		if data, err := webFS.ReadFile("web/" + name); err == nil {
			switch {
			case strings.HasSuffix(name, ".png"):
				w.Header().Set("Content-Type", "image/png")
			case strings.HasSuffix(name, ".jpg"), strings.HasSuffix(name, ".jpeg"):
				w.Header().Set("Content-Type", "image/jpeg")
			case strings.HasSuffix(name, ".svg"):
				w.Header().Set("Content-Type", "image/svg+xml")
			case strings.HasSuffix(name, ".ico"):
				w.Header().Set("Content-Type", "image/x-icon")
			}
			w.Write(data)
			return
		}
		http.NotFound(w, r)
		return
	}
	raw, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "页面资源缺失", http.StatusInternalServerError)
		return
	}
	html := string(raw)
	// 标记 + 同源改写 shim：只有本程序提供的页面才会执行
	marker := "<script>window.__CHIRPBENCH_PROXY__=true;</script>\n<script>\n" + fetchShim + "\n</script>\n"
	if i := strings.Index(strings.ToLower(html), "</head>"); i >= 0 {
		html = html[:i] + marker + html[i:]
	} else {
		html = marker + html
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, html)
}

// fetchShim 把一切非本源的 fetch 改写成 /__proxy?u=... ，页面其余逻辑完全不用改。
const fetchShim = `(function(){
  if(!window.__CHIRPBENCH_PROXY__) return;
  var orig = window.fetch.bind(window);
  var own = location.origin;
  function rewrite(u){
    try{
      var abs = new URL(u, location.href);
      if(abs.origin === own) return u;
      return '/__proxy?u=' + encodeURIComponent(abs.href);
    }catch(e){ return u; }
  }
  window.fetch = function(input, init){
    if(typeof input === 'string') return orig(rewrite(input), init);
    if(input && typeof input === 'object' && input.url){
      var r = rewrite(input.url);
      if(r !== input.url){
        try{ return orig(new Request(r, input), init); }catch(e){ return orig(r, init); }
      }
    }
    return orig(input, init);
  };
})();`

// proxyHandler 转发请求并逐块 flush，保证流式时序不被缓冲破坏。
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("u")
	if raw == "" {
		http.Error(w, "missing u", http.StatusBadRequest)
		return
	}
	target, err := url.Parse(raw)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		http.Error(w, "bad u", http.StatusBadRequest)
		return
	}

	// 请求体先缓冲下来：POST 不可重放，连接被上游重置时要能整条重发
	var bodyBytes []byte
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		bodyBytes, _ = io.ReadAll(io.LimitReader(r.Body, 64<<20))
		r.Body.Close()
	}
	newReq := func() (*http.Request, error) {
		var body io.Reader
		if bodyBytes != nil {
			body = bytes.NewReader(bodyBytes)
		}
		return http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	}

	req, err := newReq()
	if err != nil {
		http.Error(w, "build request: "+err.Error(), http.StatusBadGateway)
		return
	}

	copyHeaders := func(dst *http.Request) {
		// 透传业务头（Authorization / Content-Type 等），剥掉会误导上游的头
		for k, vs := range r.Header {
			switch strings.ToLower(k) {
			case "host", "origin", "referer", "connection", "keep-alive",
				"proxy-authenticate", "proxy-authorization", "te", "trailer",
				"transfer-encoding", "upgrade", "accept-encoding", "content-length":
				continue
			}
			for _, v := range vs {
				dst.Header.Add(k, v)
			}
		}
		// 强制不压缩：避免解压缓冲影响首字计时
		dst.Header.Set("Accept-Encoding", "identity")
	}
	copyHeaders(req)

	// 连接被上游/系统悄悄关掉时，POST 必须整条重发；Go 只会自动重试幂等请求
	var resp *http.Response
	var derr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			req, _ = newReq()
			copyHeaders(req)
			time.Sleep(time.Duration(attempt) * 400 * time.Millisecond)
		}
		resp, derr = proxyClient.Do(req)
		if derr == nil {
			break
		}
		log.Printf("[proxy] 上游连接失败（第 %d 次）：%v", attempt+1, derr)
	}
	if derr != nil {
		// 让前端能明确分辨“代理转发失败”，而不是空响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error":"proxy_failed","detail":%q}`, derr.Error())
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "transfer-encoding", "connection", "content-encoding":
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

var proxyClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 300 * time.Second, // 大上下文冷启动可能很慢
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	},
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		h.ServeHTTP(w, r)
	})
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "自动打开浏览器失败，请手动访问 %s\n", u)
	}
}
