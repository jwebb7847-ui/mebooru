package main

import (
	"errors"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func recoverManagedProfile(path string) error {
	var cfg struct {
		Desktop struct {
			Managed bool `toml:"managed_acquisition"`
		} `toml:"desktop"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return err
	}
	if !cfg.Desktop.Managed {
		return errors.New("the selected profile does not use managed acquisition")
	}
	dir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return err
	}
	return acquisitionpolicy.RecoverAfterReboot(filepath.Join(dir, "managed-acquisition"))
}
