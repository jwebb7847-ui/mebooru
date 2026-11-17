package gallery

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLookPathFallsBackWhenHostPathLacksTool(t *testing.T) {
	hostPath := t.TempDir()
	fallback := t.TempDir()
	t.Setenv("PATH", hostPath)
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	fallbackTool := filepath.Join(fallback, name)
	if err := os.WriteFile(fallbackTool, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookPath("ffmpeg", fallback); got != fallbackTool {
		t.Fatalf("with a restricted PATH, got %q, want %q", got, fallbackTool)
	}
	hostTool := filepath.Join(hostPath, name)
	if err := os.WriteFile(hostTool, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookPath("ffmpeg", fallback); got != hostTool {
		t.Fatalf("with FFmpeg on PATH, got %q, want %q", got, hostTool)
	}
}
