package checkclickhouse

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// stringFields maps config keys to the struct field each populates, so FromMap
// stays a loop rather than a wall of near-identical type assertions.
func (c *ClickHouseConfig) stringFields() map[string]*string {
	return map[string]*string{
		"host":        &c.Host,
		"username":    &c.Username,
		fieldPassword: &c.Password,
		fieldDatabase: &c.Database,
		"query":       &c.Query,
	}
}

func (c *ClickHouseConfig) boolFields() map[string]*bool {
	return map[string]*bool{
		"secure":     &c.Secure,
		"tls_verify": &c.TLSVerify,
	}
}

// FromMap populates the configuration from a map.
func (c *ClickHouseConfig) FromMap(configMap map[string]any) error {
	for key, target := range c.stringFields() {
		if err := assignString(configMap, key, target); err != nil {
			return err
		}
	}

	for key, target := range c.boolFields() {
		if err := assignBool(configMap, key, target); err != nil {
			return err
		}
	}

	if err := assignPort(configMap, &c.Port); err != nil {
		return err
	}

	return assignDuration(configMap, "timeout", &c.Timeout)
}

func assignString(configMap map[string]any, key string, target *string) error {
	raw, present := configMap[key]
	if !present || raw == nil {
		return nil
	}

	value, ok := raw.(string)
	if !ok {
		return checkerdef.NewConfigError(key, "must be a string")
	}

	*target = value

	return nil
}

func assignBool(configMap map[string]any, key string, target *bool) error {
	raw, present := configMap[key]
	if !present || raw == nil {
		return nil
	}

	value, ok := raw.(bool)
	if !ok {
		return checkerdef.NewConfigError(key, "must be a boolean")
	}

	*target = value

	return nil
}

// assignPort accepts both int and float64 because a config round-tripped
// through JSON comes back with numbers as float64.
func assignPort(configMap map[string]any, target *int) error {
	raw, present := configMap["port"]
	if !present || raw == nil {
		return nil
	}

	switch value := raw.(type) {
	case int:
		*target = value
	case float64:
		*target = int(value)
	default:
		return checkerdef.NewConfigError("port", "must be a number")
	}

	return nil
}

func assignDuration(configMap map[string]any, key string, target *time.Duration) error {
	raw, present := configMap[key]
	if !present || raw == nil {
		return nil
	}

	value, ok := raw.(string)
	if !ok {
		return checkerdef.NewConfigError(key, "must be a string")
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return checkerdef.NewConfigError(key, "must be a valid duration string")
	}

	*target = duration

	return nil
}
