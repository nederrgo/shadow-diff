package storage

// ShadowTestNameFromMetadata returns metadata override or fallback name.
func ShadowTestNameFromMetadata(meta map[string]string, fallback string) string {
	if meta != nil {
		if v := meta["shadow_test_name"]; v != "" {
			return v
		}
	}
	if fallback != "" {
		return fallback
	}
	return defaultShadowTest
}
