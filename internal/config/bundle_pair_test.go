package config

import (
	"testing"

	"github.com/jwebb7847-ui/mebooru/shared/apitoken"
)

func TestPristineBundledPair(t *testing.T) {
	if !Default().PristineBundledPair() {
		t.Fatal("default profile should be eligible")
	}
	cases := map[string]func(*Config){
		"setup completed":    func(c *Config) { c.SetupDone = true },
		"nonlocal bind":      func(c *Config) { c.Server.BindAddress = "0.0.0.0:8455" },
		"external base":      func(c *Config) { c.Server.BaseURL = "https://example.invalid" },
		"external peer":      func(c *Config) { c.Monloader.APIURL = "http://example.invalid" },
		"external frame":     func(c *Config) { c.Server.MonloaderURL = "https://example.invalid" },
		"paused":             func(c *Config) { c.Monloader.Paused = true },
		"credential present": func(c *Config) { c.Monloader.APIToken = "existing" },
		"manual token":       func(c *Config) { c.Auth.Tokens = append(c.Auth.Tokens, apitoken.Token{}) },
		"password":           func(c *Config) { c.Auth.EnablePassword = true },
		"plugin present":     func(c *Config) { c.Plugins = append(c.Plugins, PluginConfig{Name: "external"}) },
		"custom binary":      func(c *Config) { c.Desktop.MeloaderBinary = "/tmp/other" },
		"custom config":      func(c *Config) { c.Desktop.MeloaderConfig = "/tmp/other.toml" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mutate(c)
			if c.PristineBundledPair() {
				t.Fatal("nonpristine config accepted")
			}
		})
	}
}
