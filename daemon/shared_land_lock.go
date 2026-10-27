package main

func landOwnerLabel(owner string) string {
	if owner == "" {
		return "unknown"
	}
	return owner
}
