package credentials

// SecretFielder is an *optional* interface a checker config can implement
// to declare which top-level keys in its config map carry secrets. Going
// through an optional interface (rather than adding the method to the
// required Config interface) avoids forcing a 32-checker cascade for the
// majority of checker types that have no secrets at all.
//
// Implementations should return a stable, type-level list — not per-instance.
type SecretFielder interface {
	SecretFields() []string
}

// SecretFieldsFor returns the secret keys for a config, defaulting to the
// empty list when the config does not implement SecretFielder.
func SecretFieldsFor(cfg any) []string {
	if cfg == nil {
		return nil
	}

	if sf, ok := cfg.(SecretFielder); ok {
		return sf.SecretFields()
	}

	return nil
}

// SplitConfig partitions a config map into (public, private) using the
// provided list of secret keys. Keys not in the list stay public. An empty
// or nil secrets list returns the input as public with an empty private map.
func SplitConfig(full map[string]any, secrets []string) (map[string]any, map[string]any) {
	if len(full) == 0 {
		return map[string]any{}, map[string]any{}
	}

	secretSet := make(map[string]struct{}, len(secrets))
	for _, k := range secrets {
		secretSet[k] = struct{}{}
	}

	public := make(map[string]any, len(full))
	private := make(map[string]any)

	for key, value := range full {
		if _, isSecret := secretSet[key]; isSecret {
			private[key] = value

			continue
		}

		public[key] = value
	}

	return public, private
}

// MergeConfig returns a new map equal to public ∪ private, with private
// values winning on conflict (the encrypted side is the source of truth).
func MergeConfig(public, private map[string]any) map[string]any {
	merged := make(map[string]any, len(public)+len(private))
	for k, v := range public {
		merged[k] = v
	}

	for k, v := range private {
		merged[k] = v
	}

	return merged
}
