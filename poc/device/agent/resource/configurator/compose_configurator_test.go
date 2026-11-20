package configurator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func composeCpuPlanFor(component string, cpus ...int) model.CpuPlan {
	return model.CpuPlan{
		Component: model.ComponentRef(component),
		Cpus:      cpus,
	}
}

func TestRewriteComposeYamlBindsToSingleService(t *testing.T) {
	testCases := []struct {
		name     string
		source   string
		plan     model.CpuPlan
		contains []string
	}{
		{
			name: "service named differently from the component",
			source: `services:
  cyclictest:
    image: cyclictest:latest
`,
			plan:     composeCpuPlanFor("cyclictest_compose", 8, 9),
			contains: []string{"cpuset: 8-9", "TEST_CPUSET: 8-9"},
		},
		{
			name: "existing cpuset is overwritten",
			source: `services:
  stress:
    image: stress:latest
    cpuset: "1"
`,
			plan:     composeCpuPlanFor("stressng_compose", 4),
			contains: []string{"cpuset: \"4\""},
		},
		{
			name: "existing environment mapping is preserved",
			source: `services:
  stress:
    image: stress:latest
    environment:
      EXISTING: keep
`,
			plan:     composeCpuPlanFor("stressng_compose", 4, 5),
			contains: []string{"EXISTING: keep", "TEST_CPUSET: 4-5"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := rewriteComposeYaml(strings.NewReader(testCase.source), &out, testCase.plan); err != nil {
				t.Fatalf("RewriteComposeYaml: %v", err)
			}

			for _, want := range testCase.contains {
				if !strings.Contains(out.String(), want) {
					t.Errorf("rewritten compose missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRewriteComposeYamlAppliesToMultipleServices(t *testing.T) {
	source := `services:
  first:
    image: first:latest
  second:
    image: second:latest
`
	var out bytes.Buffer
	err := rewriteComposeYaml(strings.NewReader(source), &out, composeCpuPlanFor("component", 1, 2))
	if err != nil {
		t.Fatalf("RewriteComposeYaml: %v", err)
	}

	result := out.String()
	if strings.Count(result, "cpuset: 1-2") != 2 {
		t.Errorf("expected 2 occurrences of 'cpuset: 1-2', got:\n%s", result)
	}
	if strings.Count(result, "TEST_CPUSET: 1-2") != 2 {
		t.Errorf("expected 2 occurrences of 'TEST_CPUSET: 1-2', got:\n%s", result)
	}
}

func TestRewriteComposeYamlRejectsInvalidServices(t *testing.T) {
	testCases := []struct {
		name   string
		source string
	}{
		{
			name: "no services key",
			source: `version: "3"
`,
		},
		{
			name: "empty services map",
			source: `services: {}
`,
		},
		{
			name: "services is not a mapping",
			source: `services: "not-a-map"
`,
		},
		{
			name:   "empty compose yaml",
			source: ``,
		},
		{
			name: "non-mapping root",
			source: `- item1
- item2
`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			err := rewriteComposeYaml(strings.NewReader(testCase.source), &out, composeCpuPlanFor("component", 1))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestRewriteComposeYamlSupportsListFormEnvironment(t *testing.T) {
	source := `services:
  stress:
    image: stress:latest
    environment:
      - EXISTING=keep
      - TEST_CPUSET=0
`
	var out bytes.Buffer
	if err := rewriteComposeYaml(strings.NewReader(source), &out, composeCpuPlanFor("component", 1)); err == nil {
		t.Fatal("expected an error for list-form environment, got nil")
	}
}

func TestRewriteComposeYamlCopiesSourceWhenPlanHasNoCpus(t *testing.T) {
	source := `services:
  first:
    image: first:latest
  second:
    image: second:latest
`
	var out bytes.Buffer
	if err := rewriteComposeYaml(strings.NewReader(source), &out, model.CpuPlan{}); err != nil {
		t.Fatalf("RewriteComposeYaml: %v", err)
	}
	if out.String() != source {
		t.Errorf("expected the source copied verbatim, got:\n%s", out.String())
	}
}

func TestComposeConfiguratorApplyModifiesFileInPlace(t *testing.T) {
	composePath := filepath.Join(t.TempDir(), "docker-compose.yaml")
	source := `networks:
  app-net:
    driver: bridge
services:
  stress:
    image: stress:latest
    ports:
      - "8080:80"
    environment:
      EXISTING: keep
`
	if err := os.WriteFile(composePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write compose file: %v", err)
	}

	configurator := NewComposeConfigurator()
	if err := configurator.Apply(composeCpuPlanFor("stressng_compose", 2, 3), composePath); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	updated, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read updated compose file: %v", err)
	}

	result := string(updated)
	for _, want := range []string{
		"networks:",
		"app-net:",
		"8080:80",
		"cpuset: 2-3",
		"EXISTING: keep",
		"TEST_CPUSET: 2-3",
	} {
		if !strings.Contains(result, want) {
			t.Errorf("updated compose file missing %q:\n%s", want, result)
		}
	}
}
