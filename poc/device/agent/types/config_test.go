package types

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestThatConfigIsLoadedFromDisk(t *testing.T) {

	manifest, err := LoadCapabilities("../config/capabilities.json")
	assert.Nil(t, err, "error should be nil")
	assert.NotNil(t, manifest.Labels)
	k := *manifest.Labels
	str, err := k["northstarida.com/hypervisor"].AsDeviceCapabilitiesManifestLabels0()
	assert.Nil(t, err, "error should be nil")
	assert.Equal(t, "hyper-v", str, "hypervisor value should be equal")
}

func TestLoadConfigRTFeatureFlag(t *testing.T) {
	cfg, err := LoadConfig("../config/config.yaml")
	require.NoError(t, err)
	assert.False(t, cfg.RT.Enabled, "default config.yaml should have rt.enabled=false")

	tmpDir := t.TempDir()
	enabledCfgPath := filepath.Join(tmpDir, "config-rt-enabled.yaml")
	data, err := os.ReadFile("../config/config.yaml")
	require.NoError(t, err)

	enabledYAML := string(data) + "\nrt:\n  enabled: true\n"
	require.NoError(t, os.WriteFile(enabledCfgPath, []byte(enabledYAML), 0o600))

	enabledCfg, err := LoadConfig(enabledCfgPath)
	require.NoError(t, err)
	assert.True(t, enabledCfg.RT.Enabled, "rt.enabled=true should be parsed as true")
}

