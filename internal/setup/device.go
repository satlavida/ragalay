package setup

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Detection picks the torch variant for this machine before torch is
// installed, from cheap OS-level probes. Setup verifies the choice after
// installing and falls back to CPU if the accelerator does not work.
type Detection struct {
	Variant string `json:"variant"`
	Reason  string `json:"reason"`
}

// Detect chooses a torch variant. override ("cpu", "cuda", "mps",
// "rocm-gfx1201", ...) skips detection.
func Detect(ctx context.Context, override string) Detection {
	if override != "" && override != "auto" {
		return Detection{override, "chosen with --device"}
	}
	return detect(runtime.GOOS, runtime.GOARCH, probe(ctx, "nvidia-smi", "-L"), gpuNames(ctx))
}

// detect is the pure decision, separated for tests.
func detect(goos, goarch, nvidiaSMI string, gpus []string) Detection {
	if strings.Contains(nvidiaSMI, "GPU ") && (goos == "windows" || goos == "linux") {
		return Detection{"cuda", "NVIDIA GPU found (" + firstLine(nvidiaSMI) + ")"}
	}
	if goos == "darwin" && goarch == "arm64" {
		return Detection{"mps", "Apple Silicon (Metal)"}
	}
	if goos == "windows" && goarch == "amd64" {
		for _, g := range gpus {
			u := strings.ToUpper(g)
			switch {
			case strings.Contains(u, "RX 9070"):
				return Detection{"rocm-gfx1201", g + " (AMD ROCm preview)"}
			case strings.Contains(u, "RX 9060"):
				return Detection{"rocm-gfx1200", g + " (AMD ROCm preview, untested)"}
			}
		}
	}
	reason := "no supported GPU found; indexing will use the CPU (slower)"
	if len(gpus) > 0 {
		reason = "GPU " + strings.Join(gpus, ", ") + " is not supported yet; indexing will use the CPU (slower)"
	}
	return Detection{"cpu", reason}
}

func gpuNames(ctx context.Context) []string {
	var out string
	switch runtime.GOOS {
	case "windows":
		out = probe(ctx, "powershell", "-NoProfile", "-Command",
			"Get-CimInstance Win32_VideoController | ForEach-Object { $_.Name }")
	case "linux":
		out = probe(ctx, "sh", "-c", "lspci 2>/dev/null | grep -Ei 'vga|3d|display'")
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names
}

func probe(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
