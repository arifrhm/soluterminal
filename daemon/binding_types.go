package main

// roleBinding picks how a role runs. PermissionPreset is a NAME — flag
// strings never cross API boundaries (same stance as schedules_task.go).
type roleBinding struct {
	Runtime          string `json:"runtime"`
	Model            string `json:"model,omitempty"`
	Effort           string `json:"effort,omitempty"`
	PermissionPreset string `json:"permissionPreset,omitempty"`
	CacheTTL         string `json:"cacheTtl,omitempty"`
}
