package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/jwebb7847-ui/mebooru/shared/fsx"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/monbooru/monbooru/internal/web"
)

func sourceOptionsPath(dir, source string) string {
	return filepath.Join(dir, "extractor-"+source+".json")
}

func prepareSourceOptions(dir string, cfg *windowregistry.LiveConfig) error {
	body, err := os.ReadFile(cfg.PolicyConfig)
	if err != nil {
		return err
	}
	var conf liveSettings
	if err := conf.decode(body); err != nil {
		return errors.New("The acquisition settings could not be read.")
	}
	cfg.SourceOptions = make(map[string]string)
	for _, site := range conf.Sites {
		if !windowregistry.ValidLiveSource(site.Name) {
			continue
		}
		if _, duplicate := cfg.SourceOptions[site.Name]; duplicate {
			cfg.SourceOptions[site.Name] = "-"
			continue
		}
		options := make(map[string]any)
		password := cmp.Or(site.Password, site.APIKey)
		if site.Username != "" && password != "" {
			options["username"], options["password"] = site.Username, password
		}
		for name, value := range map[string]string{"api-key": site.APIKey, "user-id": site.UserID, "user-agent": site.Agent} {
			if value != "" {
				options[name] = value
			}
		}
		if site.Cookies != "" {
			cookies := site.Cookies
			if !filepath.IsAbs(cookies) {
				cookies = filepath.Join(filepath.Dir(cfg.PolicyConfig), cookies)
			}
			fi, err := os.Stat(cookies)
			if err != nil || !fi.Mode().IsRegular() {
				cfg.SourceOptions[site.Name] = "-"
				continue
			}
			options["cookies"] = cookies
		}
		if !addManagedSourceOptions(site.Name, site.Options, options) {
			cfg.SourceOptions[site.Name] = "-"
			continue
		}
		if len(options) == 0 {
			continue
		}
		path := sourceOptionsPath(dir, site.Name)
		err := fsx.WriteDurable(path, func(file *os.File) error {
			return json.NewEncoder(file).Encode(map[string]any{"extractor": map[string]any{site.Name: options}})
		})
		if err != nil {
			return err
		}
		hash, err := windowregistry.RuntimeDigest(path)
		if err != nil {
			return err
		}
		cfg.SourceOptions[site.Name] = hash
	}
	for _, source := range []string{"derpibooru", "danbooru", "e621"} {
		if hash := cfg.SourceOptions[source]; hash == "" || hash == "-" {
			if err := os.Remove(sourceOptionsPath(dir, source)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func addManagedSourceOptions(source, raw string, options map[string]any) bool {
	if raw == "" {
		return true
	}
	var custom map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &custom) != nil || custom == nil {
		return false
	}
	for key, value := range custom {
		switch {
		case source == "derpibooru" && key == "comments":
			var comments bool
			if json.Unmarshal(value, &comments) != nil {
				return false
			}
			options[key] = comments
		case source == "derpibooru" && key == "filter":
			var filter int
			if json.Unmarshal(value, &filter) != nil || filter <= 0 {
				return false
			}
			options[key] = filter
		default:
			return false
		}
	}
	return true
}

func (a *managedApplication) SaveAcquisitionSource(ctx context.Context, source web.AcquisitionSource) error {
	if a.owner.live == nil || !windowregistry.ValidLiveSource(source.Name) {
		return errors.New("Choose a supported source.")
	}
	if !addManagedSourceOptions(source.Name, source.Options, make(map[string]any)) {
		return errors.New("Managed custom options support only Derpibooru's numeric filter and comments toggle.")
	}
	if source.Cookies != "" {
		if !filepath.IsAbs(source.Cookies) {
			return errors.New("Enter an absolute path to a local cookie file.")
		}
		if fi, err := os.Stat(source.Cookies); err != nil || !fi.Mode().IsRegular() {
			return errors.New("The cookie file is unavailable.")
		}
	}
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := filepath.EvalSymlinks(a.owner.live.PolicyConfig)
	if err != nil {
		return errors.New("The acquisition settings file is unavailable.")
	}
	var document map[string]any
	if _, err := toml.DecodeFile(path, &document); err != nil {
		return errors.New("The acquisition settings file could not be read.")
	}
	sites, _ := document["sites"].([]map[string]any)
	site := make(map[string]any)
	updated := make([]map[string]any, 0, len(sites)+1)
	matched := false
	for _, saved := range sites {
		if saved["name"] != source.Name {
			updated = append(updated, saved)
		} else if !matched {
			site = saved
			matched = true
		}
	}
	site["name"] = source.Name
	for k, v := range map[string]string{
		"username": source.Username, "password": source.Password, "api_key": source.Key,
		"user_id": source.UserID, "cookies": source.Cookies,
	} {
		if source.ClearCredentials {
			delete(site, k)
		}
		if v != "" {
			site[k] = v
		}
	}
	for k, v := range map[string]string{"options": source.Options, "user_agent": source.Agent} {
		if source.ClearOptions {
			delete(site, k)
		}
		if v != "" {
			site[k] = v
		}
	}
	document["sites"] = append(updated, site)
	if err := fsx.WriteDurable(path, func(f *os.File) error { return toml.NewEncoder(f).Encode(document) }); err != nil {
		return errors.New("The source settings could not be saved.")
	}
	return nil
}
