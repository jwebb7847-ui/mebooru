package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/shared/contract"
	"github.com/monbooru/monbooru/internal/shared/desktop"
	"github.com/monbooru/monbooru/internal/shared/logx"
)

// Enabled only in the paired macOS bundle.
var useBundledMeloader string

func bundledMeloader(cfg *config.Config) (string, string, error) {
	if runtime.GOOS != "darwin" || useBundledMeloader != "true" {
		return "", "", nil
	}
	binary := desktop.BundledTool("monloader")
	if binary == "" {
		return "", "", fmt.Errorf("Meloader executable missing from the app bundle")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	loaderDir := filepath.Join(configDir, "monloader")
	if err := os.MkdirAll(loaderDir, 0o700); err != nil {
		return "", "", err
	}
	loaderConfig := filepath.Join(loaderDir, "monloader.toml")
	if err := seedBundledMeloader(loaderConfig, cfg.Server.BindAddress, cfg.DefaultGallery); err != nil {
		return "", "", err
	}
	return binary, loaderConfig, nil
}

func seedBundledMeloader(path, libraryBind, gallery string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Meloader config is not a regular file")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if !desktop.IsLoopbackAddr(libraryBind) {
		return fmt.Errorf("configure the companion explicitly when Mebooru uses a network address")
	}
	libraryURL := "http://" + desktop.LoopbackAddr(libraryBind)
	loaderBind := net.JoinHostPort("127.0.0.1", contract.PortMonloader)
	data := filepath.Join(filepath.Dir(path), "data")
	contents := fmt.Sprintf("setup_done = true\n\n[server]\nbind_address = %q\nbase_url = %q\n\n[monbooru]\napi_url = %q\nweb_url = %q\ndefault_gallery = %q\n\n[gallerydl]\nconfig_path = %q\narchive_path = %q\ncookies_dir = %q\n\n[ptr]\nenabled = false\ndata_path = %q\n\n[desktop]\ntray = false\n",
		loaderBind, "http://"+loaderBind, libraryURL, libraryURL, gallery,
		filepath.Join(data, "gallery-dl.json"), filepath.Join(data, "gallery-dl-archive.sqlite"),
		filepath.Join(data, "cookies"), filepath.Join(data, "ptr"))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.WriteString(contents)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func launchMeloader(binary, configPath string, bundled bool) (func(), error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("start Meloader separately on %s", runtime.GOOS)
	}
	if binary == "" || configPath == "" {
		return nil, fmt.Errorf("Meloader launch requires both an executable and a config path")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Meloader config must be a regular file")
	}
	args := []string{"-desktop=false", "-config", configPath}
	if bundled {
		args = []string{"-desktop=true", "-no-browser", "-config", configPath}
	}
	cmd := exec.Command(binary, args...)
	if bundled {
		paths := []string{"/opt/homebrew/bin", "/usr/local/bin"}
		if inherited := os.Getenv("PATH"); inherited != "" {
			paths = append([]string{inherited}, paths...)
		}
		cmd.Env = append(os.Environ(), "PATH="+strings.Join(paths, string(os.PathListSeparator)))
	}
	desktop.HideConsole(cmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			logx.Warnf("Meloader exited: %v", err)
		}
	}()
	return sync.OnceFunc(func() {
		select {
		case <-done:
			return
		default:
		}
		// Keep the library serving while Meloader drains any active ingest.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
		}
	}), nil
}
