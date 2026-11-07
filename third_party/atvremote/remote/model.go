package remote

// DeviceInfo holds device information reported by the Android TV.
type DeviceInfo struct {
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	SWVersion    string `json:"sw_version"`
}

// VolumeInfo holds volume information reported by the Android TV.
type VolumeInfo struct {
	Level uint32 `json:"level"`
	Max   uint32 `json:"max"`
	Muted bool   `json:"muted"`
}
