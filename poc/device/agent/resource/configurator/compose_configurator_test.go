package configurator

import (
	"bytes"
	"strings"
	"testing"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

func cpuPlanFor(component string, cpus ...int) model.CpuPlan {
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
			plan:     cpuPlanFor("cyclictest_compose", 8, 9),
			contains: []string{"cpuset: 8-9", "TEST_CPUSET: 8-9"},
		},
		{
			name: "existing cpuset is overwritten",
			source: `services:
  stress:
    image: stress:latest
    cpuset: "1"
`,
			plan:     cpuPlanFor("stressng_compose", 4),
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
			plan:     cpuPlanFor("stressng_compose", 4, 5),
			contains: []string{"EXISTING: keep", "TEST_CPUSET: 4-5"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := RewriteComposeYaml(strings.NewReader(testCase.source), &out, testCase.plan); err != nil {
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
	err := RewriteComposeYaml(strings.NewReader(source), &out, cpuPlanFor("component", 1, 2))
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
			err := RewriteComposeYaml(strings.NewReader(testCase.source), &out, cpuPlanFor("component", 1))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestRewriteComposeYamlRejectsListFormEnvironment(t *testing.T) {
	source := `services:
  stress:
    image: stress:latest
    environment:
      - EXISTING=keep
`
	var out bytes.Buffer
	if err := RewriteComposeYaml(strings.NewReader(source), &out, cpuPlanFor("component", 1)); err == nil {
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
	if err := RewriteComposeYaml(strings.NewReader(source), &out, model.CpuPlan{}); err != nil {
		t.Fatalf("RewriteComposeYaml: %v", err)
	}
	if out.String() != source {
		t.Errorf("expected the source copied verbatim, got:\n%s", out.String())
	}
}
