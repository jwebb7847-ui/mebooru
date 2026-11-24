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
	"github.com/monbooru/monbooru/internal/shared/bundlepair"
	"github.com/monbooru/monbooru/internal/shared/contract"
	"github.com/monbooru/monbooru/internal/shared/desktop"
	"github.com/monbooru/monbooru/internal/shared/logx"
)

// Enabled only in the paired macOS bundle.
var useBundledMeloader string

func bundledMeloader(cfg *config.Config) (string, string, bool, error) {
	if runtime.GOOS != "darwin" || useBundledMeloader != "true" {
		return "", "", false, nil
	}
	binary := desktop.BundledTool("monloader")
	if binary == "" {
		return "", "", false, fmt.Errorf("Meloader executable missing from the app bundle")
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", "", false, err
	}
	loaderDir := filepath.Join(configDir, "monloader")
	if err := os.MkdirAll(loaderDir, 0o700); err != nil {
		return "", "", false, err
	}
	loaderConfig := filepath.Join(loaderDir, "monloader.toml")
	created, err := seedBundledMeloaderNew(loaderConfig, cfg.Server.BindAddress, cfg.DefaultGallery)
	if err != nil {
		return "", "", false, err
	}
	return binary, loaderConfig, created, nil
}

func seedBundledMeloader(path, libraryBind, gallery string) error {
	_, err := seedBundledMeloaderNew(path, libraryBind, gallery)
	return err
}

// O_EXCL is the actual freshness proof, not a prior stat result.
func seedBundledMeloaderNew(path, libraryBind, gallery string) (bool, error) {
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("Meloader config is not a regular file")
		}
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if !desktop.IsLoopbackAddr(libraryBind) {
		return false, fmt.Errorf("configure the companion explicitly when Mebooru uses a network address")
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
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = f.WriteString(contents)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err == nil, err
}

func launchMeloader(binary, configPath string, bundled bool, bootstrap *bundleBootstrap) (func(), error) {
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
	var pipes *bootstrapPipes
	if bootstrap != nil {
		if !bundled || !trustedBundleSibling(binary) {
			return nil, fmt.Errorf("bundle bootstrap requires the bundled companion")
		}
		pipes, err = newBootstrapPipes()
		if err != nil {
			return nil, err
		}
		defer pipes.close()
		cmd.ExtraFiles = []*os.File{pipes.childRead, pipes.childWrite}
		cmd.Args = append(cmd.Args, "-bundled-bootstrap")
	}
	desktop.HideConsole(cmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if pipes != nil {
		// Only the spawned child retains these two ends after Start.
		_ = pipes.childRead.Close()
		_ = pipes.childWrite.Close()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := cmd.Wait(); err != nil {
			logx.Warnf("Meloader exited: %v", err)
		}
	}()
	if pipes != nil {
		if err := pipes.exchange(bootstrap); err != nil {
			// Avoid propagating raw persistence or transport errors, which
			// can include configuration values. Manual recovery is safe.
			logx.Warnf("bundled pairing did not complete; restart and use manual pairing if needed")
		} else {
			logx.Infof("bundled pairing credentials provisioned")
		}
	}
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

type bundleBootstrap struct {
	persist  func(bundlepair.Secrets) error
	rollback func(bundlepair.Secrets) error
}

// Only the actual sibling inside the macOS app is eligible. A matching
// process name, port, external path or loopback HTTP response is not proof.
func trustedBundleSibling(binary string) bool {
	if runtime.GOOS != "darwin" || useBundledMeloader != "true" {
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	macOS := filepath.Dir(exe)
	contents := filepath.Dir(macOS)
	if filepath.Base(exe) != "monbooru" || filepath.Base(macOS) != "MacOS" ||
		filepath.Base(contents) != "Contents" ||
		filepath.Ext(filepath.Dir(contents)) != ".app" {
		return false
	}
	info, err := os.Lstat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(binary)
	return err == nil && resolved == filepath.Join(macOS, "monloader")
}

type bootstrapPipes struct {
	childRead, parentWrite *os.File
	parentRead, childWrite *os.File
}

func newBootstrapPipes() (*bootstrapPipes, error) {
	p := &bootstrapPipes{}
	var err error
	p.childRead, p.parentWrite, err = os.Pipe()
	if err != nil {
		return nil, err
	}
	p.parentRead, p.childWrite, err = os.Pipe()
	if err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

func (p *bootstrapPipes) close() {
	for _, f := range []*os.File{p.childRead, p.parentWrite, p.parentRead, p.childWrite} {
		if f != nil {
			_ = f.Close()
		}
	}
}

func (p *bootstrapPipes) exchange(b *bundleBootstrap) error {
	deadline := time.Now().Add(15 * time.Second)
	if err := p.parentRead.SetReadDeadline(deadline); err != nil {
		return err
	}
	if err := p.parentWrite.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return bundlepair.Parent(p.parentRead, p.parentWrite, b.persist, b.rollback)
}
