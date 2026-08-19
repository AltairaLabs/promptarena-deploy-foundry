package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestGetProviderInfo(t *testing.T) {
	info, err := NewProvider().GetProviderInfo(context.Background())
	if err != nil {
		t.Fatalf("GetProviderInfo: %v", err)
	}
	if info.Name != ProviderName {
		t.Errorf("Name = %q, want %q", info.Name, ProviderName)
	}
	if info.Version != Version {
		t.Errorf("Version = %q, want %q", info.Version, Version)
	}
	if len(info.Capabilities) != 0 {
		t.Errorf("Capabilities = %v, want empty until the lifecycle is implemented", info.Capabilities)
	}

	var schema map[string]any
	if err := json.Unmarshal([]byte(info.ConfigSchema), &schema); err != nil {
		t.Fatalf("ConfigSchema is not valid JSON: %v", err)
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("ConfigSchema has no required list")
	}
	want := map[string]bool{"account": true, "project": true, "image": true, "providers": true}
	for _, r := range required {
		delete(want, r.(string))
	}
	if len(want) != 0 {
		t.Errorf("ConfigSchema is missing required fields: %v", want)
	}
}

// Every lifecycle method must report ErrNotImplemented via errors.Is, not via
// message text — that is the contract callers are written against.
func TestLifecycleNotImplemented(t *testing.T) {
	ctx := context.Background()
	p := NewProvider()

	tests := []struct {
		name string
		call func() error
	}{
		{"validate", func() error { _, err := p.ValidateConfig(ctx, nil); return err }},
		{"plan", func() error { _, err := p.Plan(ctx, nil); return err }},
		{"apply", func() error { _, err := p.Apply(ctx, nil, nil); return err }},
		{"destroy", func() error { return p.Destroy(ctx, nil, nil) }},
		{"status", func() error { _, err := p.Status(ctx, nil); return err }},
		{"import", func() error { _, err := p.Import(ctx, nil); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotImplemented) {
				t.Errorf("err = %v, want ErrNotImplemented", err)
			}
		})
	}
}
