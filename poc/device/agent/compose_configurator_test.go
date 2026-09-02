package main

import (
	"bytes"
	"strings"
	"testing"
)

func cpuPlanFor(component string, cpus ...int) CpuPlan {
	return CpuPlan{Assignments: []CpuAssignment{{
		Component: ComponentRef(component),
		Cpus:      cpus,
	}}}
}

func TestRewriteComposeYAMLBindsToSingleService(t *testing.T) {
	testCases := []struct {
		name     string
		source   string
		plan     CpuPlan
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
			if err := RewriteComposeYAML(strings.NewReader(testCase.source), &out, testCase.plan); err != nil {
				t.Fatalf("RewriteComposeYAML: %v", err)
			}

			for _, want := range testCase.contains {
				if !strings.Contains(out.String(), want) {
					t.Errorf("rewritten compose missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestRewriteComposeYAMLRejectsServiceCountOtherThanOne(t *testing.T) {
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
			name: "empty services mapping",
			source: `services: {}
`,
		},
		{
			name: "two services",
			source: `services:
  first:
    image: first:latest
  second:
    image: second:latest
`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			err := RewriteComposeYAML(strings.NewReader(testCase.source), &out, cpuPlanFor("component", 1))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

// The list form of environment is valid Compose but unsupported today; pin the failure
// rather than silently producing a file the workload cannot use.
func TestRewriteComposeYAMLRejectsListFormEnvironment(t *testing.T) {
	source := `services:
  stress:
    image: stress:latest
    environment:
      - EXISTING=keep
`
	var out bytes.Buffer
	if err := RewriteComposeYAML(strings.NewReader(source), &out, cpuPlanFor("component", 1)); err == nil {
		t.Fatal("expected an error for list-form environment, got nil")
	}
}

func TestRewriteComposeYAMLCopiesSourceWhenPlanHasNoCpus(t *testing.T) {
	source := `services:
  first:
    image: first:latest
  second:
    image: second:latest
`
	var out bytes.Buffer
	if err := RewriteComposeYAML(strings.NewReader(source), &out, CpuPlan{}); err != nil {
		t.Fatalf("RewriteComposeYAML: %v", err)
	}
	if out.String() != source {
		t.Errorf("expected the source copied verbatim, got:\n%s", out.String())
	}
}
