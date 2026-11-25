package config

import "github.com/jwebb7847-ui/mebooru/shared/contract"

// PristineBundledPair is a second guard, in addition to the launcher's proof
// that both configuration files were created during this launch. Never use
// this predicate alone to infer that an existing unpaired profile is new.
func (c *Config) PristineBundledPair() bool {
	bind := "127.0.0.1:" + contract.PortMonbooru
	if c.SetupDone || c.Server.BindAddress != bind ||
		(c.Server.BaseURL != "http://localhost:"+contract.PortMonbooru &&
			c.Server.BaseURL != "http://"+bind) ||
		c.Server.MonloaderURL != "" || c.Monloader.APIURL != "" ||
		c.Monloader.APIToken != "" || c.Monloader.Paused ||
		len(c.Auth.Tokens) != 0 || c.Auth.EnablePassword ||
		len(c.Plugins) != 0 || c.Desktop.MeloaderBinary != "" ||
		c.Desktop.MeloaderConfig != "" {
		return false
	}
	for _, key := range []string{"server.bind_address", "server.base_url",
		"server.monloader_url", "monloader.api_url", "monloader.api_token",
		"auth.password_hash"} {
		if c.EnvPinned(key) != "" {
			return false
		}
	}
	return true
}
