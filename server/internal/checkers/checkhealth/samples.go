package checkhealth

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
)

// GetSampleConfigs returns one sample per supported health document format.
func (c *HealthChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	sample := func(name, slug, format, url string, headers map[string]string) checkerdef.CheckSpec {
		return checkerdef.CheckSpec{
			Name:   name,
			Slug:   slug,
			Period: time.Minute,
			Config: (&HealthConfig{
				HTTPConfig: httpconfig.HTTPConfig{URL: url, SecretHeaders: headers},
				Format:     format,
			}).GetConfig(),
		}
	}

	return []checkerdef.CheckSpec{
		sample("Laravel health (Oh Dear format)", "health-laravel", "spatie",
			"https://app.acme.com/health", map[string]string{"oh-dear-health-check-secret": "change-me"}),
		sample("Spring Boot actuator health", "health-spring", "spring",
			"https://api.acme.com/actuator/health", nil),
		sample("ASP.NET Core health checks", "health-aspnet", "aspnet",
			"https://api.acme.com/healthz", nil),
		sample("IETF health+json", "health-ietf", "ietf",
			"https://api.acme.com/health", nil),
	}
}
