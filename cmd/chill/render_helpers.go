package main

func display(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func compactDigest(value string) string {
	if value == "" {
		return "-"
	}
	const visibleLength = len("sha256:") + 12
	if len(value) <= visibleLength {
		return value
	}
	return value[:visibleLength] + "..."
}
