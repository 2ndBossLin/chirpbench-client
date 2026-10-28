package main

// 硬件自动识别：客户端在本机跑，所以能直接读系统信息。
// 识别结果只是"预填建议"，页面上用户可以手改。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type hwInfo struct {
	MachineID string `json:"machine_id"`
	GPUModel string `json:"gpu_model"`
	GPUCount int    `json:"gpu_count"`
	CPU      string `json:"cpu"`
	RAMSize  string `json:"ram_size"`
	RAMSpeed string `json:"ram_speed"`
	Chipset  string `json:"chipset"`
	OS       string `json:"os"`
}

// run 执行外部命令，带超时，返回 stdout（失败返回空串）
func run(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func detectGPU() (string, int) {
	out := run("nvidia-smi", "--query-gpu=name", "--format=csv,noheader")
	var names []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			names = append(names, l)
		}
	}
	if len(names) > 0 {
		return normalizeGPUName(names[0]), len(names)
	}
	// Linux: lspci 兜底
	out = run("sh", "-c", "lspci | grep -iE 'vga|3d|display'")
	if strings.TrimSpace(out) != "" {
		return strings.TrimSpace(strings.Split(out, "\n")[0]), 1
	}
	return "", 0
}

var (
	reBracket = regexp.MustCompile(`\s*\[.*?\]\s*`)
	reSpaces  = regexp.MustCompile(`\s+`)
)

// normalizeGPUName 去掉驱动后缀，如 "NVIDIA GeForce RTX 5060 Ti" 保持不变，
// "NVIDIA RTX 4090 [PCIe x16]" -> "NVIDIA RTX 4090"
func normalizeGPUName(s string) string {
	s = reBracket.ReplaceAllString(s, " ")
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func detectCPU() string {
	if runtime.GOOS == "windows" {
		out := run("powershell", "-NoProfile", "-Command", "(Get-CimInstance Win32_Processor).Name")
		return strings.TrimSpace(out)
	}
	for _, l := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
		if strings.HasPrefix(l, "model name") {
			if i := strings.Index(l, ":"); i >= 0 {
				return strings.TrimSpace(l[i+1:])
			}
		}
	}
	if runtime.GOOS == "darwin" {
		return strings.TrimSpace(run("sysctl", "-n", "machdep.cpu.brand_string"))
	}
	return ""
}

func detectRAM() (string, string) {
	if runtime.GOOS == "windows" {
		// 容量
		out := run("powershell", "-NoProfile", "-Command",
			"$c=(Get-CimInstance Win32_PhysicalMemory | Measure-Object -Property Capacity -Sum).Sum; [math]::Round($c/1GB)")
		size := strings.TrimSpace(out)
		// 频率
		sp := run("powershell", "-NoProfile", "-Command",
			"$s=(Get-CimInstance Win32_PhysicalMemory | Measure-Object -Property Speed -Maximum).Maximum; $s")
		speed := strings.TrimSpace(sp)
		if size != "" {
			size += " GB"
		}
		if speed != "" && speed != "0" {
			speed = "DDR " + speed + " MT/s"
		}
		return size, speed
	}
	// Linux: MemTotal
	size := ""
	for _, l := range strings.Split(readFile("/proc/meminfo"), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				if kb, err := strconv.ParseFloat(f[1], 64); err == nil {
					gb := kb / 1024 / 1024
					size = strconv.Itoa(int(gb+0.5)) + " GB"
				}
			}
			break
		}
	}
	// 频率：dmidecode 通常要 root，失败就用 /sys 或留空
	speed := ""
	if out := run("dmidecode", "-t", "memory"); out != "" {
		re := regexp.MustCompile(`(?m)^\s*Speed:\s*(\d+)\s*(?:MT/s|MHz)`)
		best := 0
		for _, m := range re.FindAllStringSubmatch(out, -1) {
			if v, err := strconv.Atoi(m[1]); err == nil && v > best {
				best = v
			}
		}
		if best > 0 {
			speed = "DDR " + strconv.Itoa(best) + " MT/s"
		}
	}
	return size, speed
}

func detectChipset() string {
	if runtime.GOOS == "windows" {
		out := run("powershell", "-NoProfile", "-Command",
			"$b=Get-CimInstance Win32_BaseBoard; \"$($b.Manufacturer) $($b.Product)\".Trim()")
		return strings.TrimSpace(out)
	}
	vendor := strings.TrimSpace(readFile("/sys/class/dmi/id/board_vendor"))
	name := strings.TrimSpace(readFile("/sys/class/dmi/id/board_name"))
	s := strings.TrimSpace(vendor + " " + name)
	if s == "" {
		s = strings.TrimSpace(readFile("/sys/class/dmi/id/product_name"))
	}
	return s
}

// machineID 由主板/系统唯一值 + 硬件型号算 hash，只上报 hash，不上报原始序列号。
// 用途：同一台机器的成绩去重、同一台机器多账号只算一票。
func machineID(gpu string, cpu string) string {
	parts := []string{
		strings.TrimSpace(readFile("/etc/machine-id")),
		strings.TrimSpace(readFile("/var/lib/dbus/machine-id")),
		strings.TrimSpace(readFile("/sys/class/dmi/id/board_serial")),
		strings.TrimSpace(readFile("/sys/class/dmi/id/product_uuid")),
		strings.TrimSpace(readFile("/sys/class/dmi/id/board_name")),
		cpu, gpu, runtime.GOOS,
	}
	if runtime.GOOS == "windows" {
		parts = append(parts,
			strings.TrimSpace(run("powershell", "-NoProfile", "-Command",
				"(Get-ItemProperty 'HKLM:\\SOFTWARE\\Microsoft\\Cryptography' -Name MachineGuid).MachineGuid")),
			strings.TrimSpace(run("powershell", "-NoProfile", "-Command",
				"(Get-CimInstance Win32_BaseBoard).SerialNumber")))
	}
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])[:32]
}

func detectHW() hwInfo {
	gpu, n := detectGPU()
	ramSize, ramSpeed := detectRAM()
	cpu := detectCPU()
	return hwInfo{
		MachineID: machineID(gpu, cpu),
		GPUModel: gpu,
		GPUCount: n,
		CPU:      cpu,
		RAMSize:  ramSize,
		RAMSpeed: ramSpeed,
		Chipset:  detectChipset(),
		OS:       runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// hwHandler 返回本机硬件信息，页面用来预填（用户可改）
func hwHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(detectHW())
}
