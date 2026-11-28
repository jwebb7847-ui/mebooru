// Command monbooru serves the galleries, in the server, desktop or
// portable profile.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jwebb7847-ui/mebooru/shared/desktop"
	"github.com/jwebb7847-ui/mebooru/shared/logx"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/jobs"
	"github.com/monbooru/monbooru/internal/library"
	internalweb "github.com/monbooru/monbooru/internal/web"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	// Private probe entries must precede console attachment and all app init.
	if dispatchManagedProbe(os.Args[1:]) {
		return
	}
	// Before anything prints: a Windows GUI build starts with no console.
	desktop.AttachConsole()

	if len(os.Args) >= 2 && os.Args[1] == "tagger-worker" {
		runWorker(os.Args[2:])
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "healthcheck" {
		runHealthcheck(os.Args[2:])
		return
	}

	configPath := flag.String("config", "./monbooru.toml", "path to monbooru.toml config file")
	hashPassword := flag.String("hash-password", "", "print bcrypt hash of the given password and exit")
	desktopMode := flag.Bool("desktop", defaultDesktop == "true", "desktop profile: OS-native paths, a log file, a browser at startup")
	noBrowser := flag.Bool("no-browser", false, "with -desktop, do not open a browser at startup")
	meloaderBinary := flag.String("meloader", "", "start this Meloader executable alongside Mebooru")
	meloaderConfig := flag.String("meloader-config", "", "config file for the Meloader companion")
	managedDisposable := flag.String("managed-disposable", "", "start Loader with a prepared disposable managed-acquisition fixture")
	recoverManaged := flag.Bool("recover-managed-after-reboot", false, "recover an existing managed acquisition profile offline after restarting this Mac")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionLine())
		return
	}
	if *recoverManaged {
		if explicitFlag("config", *configPath) == "" || *managedDisposable != "" {
			fatalf("recovery requires -config with the existing managed profile")
		}
		if err := recoverManagedProfile(*configPath); err != nil {
			fatalf("recovering managed acquisition: %v", err)
		}
		fmt.Println("Managed acquisition is ready to restart offline.")
		return
	}
	if *hashPassword != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*hashPassword), bcrypt.DefaultCost)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error hashing password: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(hash))
		return
	}

	// Registered first so it runs last, once the other defers have
	// flushed the pools, stopped the watchers and closed the log.
	exitCode := 0
	defer func() {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()

	// Runs after the defers below, so the new process finds the port, the
	// database and the log file released.
	restartOnExit := false
	var layout desktop.Layout
	defer func() {
		if !restartOnExit {
			return
		}
		if err := desktop.Relaunch(); err != nil {
			if layout.LogDir != "" {
				if f, ferr := desktop.OpenLog(layout.LogDir, appName); ferr == nil {
					defer func() { _ = f.Close() }()
				}
			}
			reportFatal("restart: " + err.Error())
		}
	}()

	var seed func(*config.Config)
	var profile internalweb.Desktop
	var fixture managedqa.Setup
	var cfg *config.Config
	var err error
	newBundleProfile := false
	if *managedDisposable != "" {
		if explicitFlag("config", *configPath) != "" || *meloaderBinary != "" || *meloaderConfig != "" {
			fatalf("the managed disposable fixture uses its own application profile")
		}
		*desktopMode = false
		*configPath = filepath.Join(*managedDisposable, "monbooru.toml")
		cfg, fixture, err = disposableManagedConfig(*managedDisposable)
		if err != nil {
			fatalf("opening disposable managed profile: %v", err)
		}
	} else if *desktopMode {
		dialogOnFatal = true
		explicitProfile := explicitFlag("config", *configPath)
		resolved, err := desktop.Resolve(appName, explicitProfile)
		if err != nil {
			fatalf("resolving the desktop paths: %v", err)
		}
		layout = resolved
		if explicitProfile != "" {
			layout.DataDir = filepath.Join(layout.ConfigDir, "data")
			layout.LogDir = filepath.Join(layout.DataDir, "logs")
		}
		*configPath = layout.ConfigPath
		// Claim before logging creates this directory. An old profile with
		// its TOML removed is not fresh and must never regain trust.
		if useBundledMeloader == "true" && defaultManagedAcquisition != "true" && !layout.Portable &&
			explicitProfile == "" {
			var claimErr error
			newBundleProfile, claimErr = claimNewProfileDir(layout.ConfigDir)
			if claimErr != nil {
				logx.Warnf("could not claim a fresh desktop profile")
			}
		}
		profile = internalweb.Desktop{Active: true, LogDir: layout.LogDir, ExplicitProfile: explicitProfile != ""}
		// Before config load, so every fatal below reaches the file too.
		if f, err := desktop.OpenLog(layout.LogDir, appName); err != nil {
			logx.Warnf("could not open a log file under %s: %v", layout.LogDir, err)
		} else {
			defer func() { _ = f.Close() }()
		}
		seed = desktopSeed(layout, explicitProfile != "")
	} else if !inContainer() {
		seed = hostSeed(*configPath)
	}

	_, statErr := os.Stat(*configPath)
	freshConfig := os.IsNotExist(statErr)
	_, lstatErr := os.Lstat(*configPath)
	newBundleProfile = newBundleProfile && os.IsNotExist(lstatErr)

	load := config.LoadWithDefaults
	if layout.Portable {
		load = config.LoadPortable
	}
	if cfg == nil {
		if *desktopMode && freshConfig && defaultManagedAcquisition == "true" && *meloaderConfig == "" {
			if err := seedBundledPolicy(filepath.Join(filepath.Dir(*configPath), bundledPolicyConfig)); err != nil {
				logx.Warnf("creating acquisition settings: %v", err)
			}
		}
		cfg, err = load(*configPath, seed)
		if err != nil {
			fatalf("loading config: %v", err)
		}
	}
	if layout.Portable {
		portableGallery(layout, cfg)
	}
	logx.Set(cfg.Log.Level)
	logx.Infof("config: bind=%s galleries=%d default=%q models=%s log=%s",
		cfg.Server.BindAddress, len(cfg.Galleries), cfg.DefaultGallery, cfg.Paths.ModelPath, cfg.Log.Level)

	var ln net.Listener
	if *desktopMode {
		ln, err = desktop.Listen(appName, cfg.Server.BindAddress, bindWait)
		if errors.Is(err, desktop.ErrRunning) {
			if err := desktop.HandOver(appName, cfg.Server.BindAddress, !*noBrowser); err != nil {
				logx.Warnf("%v", err)
			}
			return
		}
		if err != nil {
			fatalf("%v", err)
		}
	} else if ln, err = net.Listen("tcp", cfg.Server.BindAddress); err != nil {
		fatalf("HTTP server: %v", err)
	}

	jobManager := jobs.NewManager()

	srv, err := internalweb.NewServer(cfg, *configPath, jobManager, profile)
	if err != nil {
		if freshConfig && inContainer() {
			logx.Warnf("Mebooru wrote %s with default settings meant for the docker image.", *configPath)
			logx.Warnf("edit gallery_path (your images), paths.data_path (db + thumbnails) and paths.model_path (optional auto-taggers), then run it again. docs: %s", internalweb.DocURL)
		}
		fatalf("creating web server: %v", err)
	}
	defer srv.Close()
	var managed *managedApplication
	managedMode := *managedDisposable != "" || cfg.Desktop.ManagedAcquisition
	migrationPending := !managedMode && *desktopMode && defaultManagedAcquisition == "true" && *meloaderBinary == "" && *meloaderConfig == ""
	if migrationPending {
		srv.OfferLegacyMigration(func(ctx context.Context, legacyConfig, state, defaultGallery string) (string, error) {
			return migrateLegacyAcquisition(ctx, *configPath, legacyConfig, state, defaultGallery)
		})
	}
	if managedMode {
		start := func() error {
			dir := *managedDisposable
			var live *windowregistry.LiveConfig
			if dir == "" {
				if *meloaderBinary != "" {
					return errors.New("managed acquisition uses the installed Meloader sibling")
				}
				settings := cmp.Or(*meloaderConfig, cfg.Desktop.MeloaderConfig)
				if settings != "" && !filepath.IsAbs(settings) {
					settings = filepath.Join(filepath.Dir(*configPath), settings)
				}
				options, err := readLiveConfig(settings)
				if err != nil {
					return fmt.Errorf("managed acquisition settings: %w", err)
				}
				dir, err = prepareLiveDirectory(*configPath)
				if err != nil {
					return fmt.Errorf("managed acquisition profile: %w", err)
				}
				selectRuntimeWheel(dir, &options)
				live = &options
			}
			return srv.AttachManagedLoader(func(set *library.Set) (internalweb.ManagedLoader, error) {
				var err error
				managed, err = openManagedApplication(dir, fixture, live, set, cfg.Gallery)
				return managed, err
			})
		}
		if err := start(); err != nil {
			if *managedDisposable != "" || errors.Is(err, acquisitionpolicy.ErrProfileOwned) {
				fatalf("starting managed acquisition: %v", err)
			}
			logx.Errorf("acquisition unavailable; local library remains available: %v", err)
			srv.DisableManagedLoader()
		} else {
			defer func() {
				if err := managed.Close(); err != nil {
					reportFatal("shutting down managed acquisition: " + err.Error())
					exitCode = 1
				}
			}()
		}
	}

	bundledCompanion := false
	bundledConfigCreated := false
	if !managedMode && !migrationPending && *desktopMode && *meloaderBinary == "" && *meloaderConfig == "" {
		*meloaderBinary = cfg.Desktop.MeloaderBinary
		*meloaderConfig = cfg.Desktop.MeloaderConfig
		if *meloaderBinary == "" && *meloaderConfig == "" {
			*meloaderBinary, *meloaderConfig, bundledConfigCreated, err = bundledMeloader(cfg)
			bundledCompanion = err == nil && *meloaderBinary != ""
			if err != nil {
				logx.Errorf("preparing bundled Meloader: %v", err)
			}
		}
	}

	// Freshness cannot be inferred from missing tokens: revocation and a
	// partial reset can also leave them absent. Only this first launch may
	// bootstrap, after the library has won its own listener.
	var bootstrap *bundleBootstrap
	if newBundleProfile && bundledConfigCreated && bundledCompanion &&
		!layout.Portable && explicitFlag("config", *configPath) == "" &&
		cfg.PristineBundledPair() && trustedBundleSibling(*meloaderBinary) {
		bootstrap = &bundleBootstrap{
			persist:  srv.PersistBundledPair,
			rollback: srv.RollbackBundledPair,
		}
	}
	stopMeloader := func() {}
	if !managedMode && !migrationPending && (*meloaderBinary != "" || *meloaderConfig != "") {
		stop, err := launchMeloader(*meloaderBinary, *meloaderConfig, bundledCompanion, bootstrap)
		if err != nil {
			logx.Errorf("starting Meloader: %v", err)
		} else {
			stopMeloader = stop
			defer stopMeloader()
		}
	}

	srv.StartWatchers()

	httpSrv := &http.Server{
		Addr:    cfg.Server.BindAddress,
		Handler: srv.Handler(),
		// Headers only: an upload or import body can take minutes on a slow link.
		ReadHeaderTimeout: 30 * time.Second,
		// No WriteTimeout: some responses run for minutes on a large library.
		IdleTimeout: 120 * time.Second,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	srvErr := make(chan error, 1)
	go func() {
		logx.Infof("Mebooru listening on %s", cfg.Server.BindAddress)
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			srvErr <- err
		}
	}()
	if managedMode {
		fmt.Printf("Managed Loader listening at http://%s/loader\n", ln.Addr())
	}

	if *desktopMode {
		if !*noBrowser {
			go func() {
				if err := desktop.OpenWhenServing(appName, cfg.Server.BindAddress, 30*time.Second); err != nil {
					logx.Warnf("%v", err)
				}
			}()
		}
		if srv.TrayEnabled() {
			trayCtx, stopTray := context.WithCancel(context.Background())
			trayDone := make(chan struct{})
			// The icon stays on screen until the tray's own loop removes it.
			defer func() {
				stopTray()
				select {
				case <-trayDone:
				case <-time.After(time.Second):
				}
			}()
			go func() {
				defer close(trayDone)
				runTray(trayCtx, srv, cfg.Server.BindAddress)
			}()
		}
	}

	select {
	case <-quit:
		logx.Infof("shutting down...")
	case <-srv.QuitRequested():
		logx.Infof("shutting down on request...")
	case err := <-srvErr:
		reportFatal("HTTP server: " + err.Error())
		exitCode = 1
	}
	stopMeloader()
	signal.Stop(quit)
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutCtx) //nolint:errcheck
	restartOnExit = srv.RestartRequested()
}
