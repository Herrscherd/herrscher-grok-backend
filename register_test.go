package grok

import (
	"errors"
	"testing"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

func grokPlugin(t *testing.T) contracts.Plugin {
	t.Helper()
	plugins := contracts.Default.Backends()
	for index := range plugins {
		if plugins[index].Manifest.Kind == contracts.VendorGrok {
			return plugins[index]
		}
	}
	t.Fatal("the grok plugin is not registered")
	return contracts.Plugin{}
}

func TestManifestDeclaresAValidGatedBackend(t *testing.T) {
	manifest := grokPlugin(t).Manifest
	if manifest.Category != contracts.CategoryBackend || manifest.Capabilities.Gate != contracts.GrainTool {
		t.Fatalf("manifest = %+v, want a backend that gates every tool call", manifest)
	}
	if err := contracts.ValidateModels(manifest.Kind, manifest.Models); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryRefusesAnUnknownEffort(t *testing.T) {
	plugin := grokPlugin(t)
	_, err := plugin.Backend(t.Context(), contracts.PluginConfig{Settings: map[string]string{settingEffort: "max"}})
	if !errors.Is(err, ErrInvalidSetting) {
		t.Fatalf("err = %v, want ErrInvalidSetting", err)
	}
	backend, err := plugin.Backend(t.Context(), contracts.PluginConfig{Settings: map[string]string{settingEffort: " high ", settingResume: "sess"}})
	if err != nil {
		t.Fatal(err)
	}
	stream, isStream := backend.(*streamResponder)
	if !isStream || stream.effort != "high" || stream.ResumeToken() != "sess" {
		t.Fatalf("backend = %+v, want the trimmed effort and the resumed session", backend)
	}
}
