package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/fsx"
	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/web"
)

// Release qualification pins the exact pure-Python wheel. An upstream
// version announcement alone never authorizes execution.
const (
	updateWheelURL = "https://files.pythonhosted.org/packages/80/e3/889fb3ac6dabd28ff432763e0414ddb7d0d6a3b38df88d6e602e185548c8/gallery_dl-1.32.21-py3-none-any.whl"
	updateWheelSHA = "f3f3e62bf3863cae6e1635045748c50fc51d52c111ca64b068d618e9334e8811"
	runtimeChoice  = "gallery-dl-version"
)

func runtimeWheelPath(dir string) string {
	return filepath.Join(dir, "gallery_dl-"+acquisitionplan.StrictQualifiedUpdateVersion+"-py3-none-any.whl")
}

func runtimeUpdateSelected(dir string) bool {
	body, err := os.ReadFile(filepath.Join(dir, runtimeChoice))
	return err == nil && string(body) == acquisitionplan.StrictQualifiedUpdateVersion+"\n"
}

func selectRuntimeWheel(dir string, cfg *windowregistry.LiveConfig) {
	if cfg.Runtime != macRuntimeLauncher() || !runtimeUpdateSelected(dir) {
		return
	}
	path := runtimeWheelPath(dir)
	if hash, err := windowregistry.RuntimeDigest(path); err == nil && hash == updateWheelSHA {
		cfg.RuntimeWheel, cfg.RuntimeWheelSHA256 = path, hash
	} else {
		logx.Warnf("gallery-dl update could not be verified; using the bundled runtime")
	}
}

func runtimeOutput(ctx context.Context, path, wheel string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, path, append([]string{"--config-ignore"}, args...)...)
	cmd.Env = append(os.Environ(), "MEBOORU_RUNTIME_WHEEL="+wheel)
	cmd.WaitDelay = time.Second
	var stderr probeOutput
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || len(stderr.Bytes()) != 0 || stderr.overflow {
		return "", errors.New("gallery-dl could not complete the local runtime check")
	}
	return strings.TrimSpace(string(out)), nil
}

func (a *managedApplication) readRuntimeStatus(settings *web.AcquisitionSettings) {
	cfg := a.owner.live
	settings.RuntimeSource = "Configured executable"
	if launcher, _ := liveRuntimePath(""); cfg.Runtime == launcher {
		settings.RuntimeSource = "Bundled with Mebooru"
	}
	settings.RuntimeUpdates = cfg.Runtime == macRuntimeLauncher()
	if cfg.RuntimeWheel != "" {
		settings.RuntimeSource = "Verified update"
	}
	a.runtimeProbe.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if version, err := runtimeOutput(ctx, cfg.Runtime, cfg.RuntimeWheel, "--version"); err == nil {
			a.runtimeVersion = version
		}
	})
	settings.RuntimeVersion = a.runtimeVersion
	settings.RuntimeReady = a.runtimeVersion == cfg.Version()
	if settings.RuntimeUpdates {
		selected := runtimeUpdateSelected(a.owner.dir)
		settings.RuntimeRollback = selected || cfg.RuntimeWheel != ""
		if selected {
			settings.RuntimeSelection = acquisitionplan.StrictQualifiedUpdateVersion
			if cfg.RuntimeWheel == "" {
				hash, err := windowregistry.RuntimeDigest(runtimeWheelPath(a.owner.dir))
				settings.RuntimeUpdateDamaged = err != nil || hash != updateWheelSHA
			}
		} else {
			settings.RuntimeSelection = acquisitionplan.StrictQualifiedVersion
		}
		settings.NeedsRestart = settings.NeedsRestart || settings.RuntimeSelection != cfg.Version() && !settings.RuntimeUpdateDamaged
	}
}

func (a *managedApplication) RuntimeAction(ctx context.Context, action string) (string, error) {
	cfg := a.owner.live
	if cfg == nil || cfg.Runtime != macRuntimeLauncher() {
		return "", web.ErrManagedImportUnsupported
	}
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	dir := a.owner.dir
	set := func(version string) error {
		return fsx.WriteDurable(filepath.Join(dir, runtimeChoice), func(f *os.File) error {
			_, err := io.WriteString(f, version+"\n")
			return err
		})
	}
	if action == "rollback" {
		if err := set(acquisitionplan.StrictQualifiedVersion); err != nil {
			return "", err
		}
		return "The bundled runtime is selected. Restart Mebooru to use it.", nil
	}
	if a.Offline() {
		return "", workerproto.ErrOffline
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	lease, err := a.manager.AcquireOnline(ctx)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	if action == "check" {
		body, err := fetchRuntimeArtifact(lease.Context, "https://pypi.org/pypi/gallery-dl/json", 2<<20)
		var release struct {
			Info struct{ Version string }
		}
		if err != nil || json.Unmarshal(body, &release) != nil || release.Info.Version == "" {
			return "", errors.New("Could not check gallery-dl releases.")
		}
		if runtimeUpdateSelected(dir) {
			return fmt.Sprintf("Latest upstream release is %s. Qualified version %s is already selected.", release.Info.Version, acquisitionplan.StrictQualifiedUpdateVersion), nil
		}
		return fmt.Sprintf("Latest upstream release is %s. Qualified update %s is available for installation.", release.Info.Version, acquisitionplan.StrictQualifiedUpdateVersion), nil
	}
	if action != "install" {
		return "", web.ErrManagedImportUnsupported
	}
	path := runtimeWheelPath(dir)
	if runtimeUpdateSelected(dir) {
		if hash, err := windowregistry.RuntimeDigest(path); err == nil && hash == updateWheelSHA {
			return "The qualified gallery-dl update is already selected.", nil
		}
	}
	body, err := fetchRuntimeArtifact(lease.Context, updateWheelURL, 4<<20)
	if err != nil {
		return "", errors.New("Could not download the qualified gallery-dl update.")
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != updateWheelSHA {
		return "", errors.New("The downloaded gallery-dl update did not match its published checksum.")
	}
	if err := fsx.WriteDurable(path, func(f *os.File) error {
		_, err := f.Write(body)
		return err
	}); err != nil {
		return "", err
	}
	version, err := runtimeOutput(lease.Context, cfg.Runtime, path, "--version")
	if err != nil || version != acquisitionplan.StrictQualifiedUpdateVersion {
		return "", errors.New("The gallery-dl update did not run as the qualified version.")
	}
	for _, source := range []struct{ site, url string }{
		{"derpibooru", "https://derpibooru.org/images/1"},
		{"danbooru", "https://danbooru.donmai.us/posts/1"},
		{"e621", "https://e621.net/posts/1"},
	} {
		out, err := runtimeOutput(lease.Context, cfg.Runtime, path, "-E", source.url)
		if err != nil {
			return "", err
		}
		if _, err := acquisitionplan.QualifiedClassification(out, source.site); err != nil {
			return "", errors.New("The update did not recognize a supported acquisition source.")
		}
	}
	if !lease.Current() {
		return "", workerproto.ErrOffline
	}
	if err := set(version); err != nil {
		return "", err
	}
	return fmt.Sprintf("Verified gallery-dl %s. Restart Mebooru to use it.", version), nil
}

func fetchRuntimeArtifact(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime release server returned HTTP %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("invalid runtime release response")
	}
	return body, nil
}
