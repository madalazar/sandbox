package configurator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	yamlv3 "gopkg.in/yaml.v3"
)

const ComposeCpuSetEnvVarName = "TEST_CPUSET"

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Extra    map[string]yamlv3.Node    `yaml:",inline"`
}

func (c composeFile) validate() error {
	if len(c.Services) == 0 {
		return fmt.Errorf("compose file must define at least one service")
	}
	return nil
}

type composeService struct {
	Cpuset      string                 `yaml:"cpuset,omitempty"`
	Environment yamlv3.Node            `yaml:"environment,omitempty"`
	Extra       map[string]yamlv3.Node `yaml:",inline"`
}

// applies a cpu plan to a compose package by updating the downloaded file in-place
type ComposeConfigurator struct{}

func NewComposeConfigurator() *ComposeConfigurator {
	return &ComposeConfigurator{}
}

// writes the plan's cpuset into the services of sourcePath in-place
func (c *ComposeConfigurator) Apply(
	plan model.CpuPlan,
	sourcePath string,
) error {
	if !plan.HasCpus() {
		return nil
	}

	cleanPath := filepath.Clean(sourcePath)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return fmt.Errorf("read compose file: %w", err)
	}

	var out bytes.Buffer
	if err := rewriteComposeYaml(bytes.NewReader(data), &out, plan); err != nil {
		return fmt.Errorf("rewrite compose yaml: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(cleanPath), ".compose-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp compose file: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, out.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write temp compose file: %w", err)
	}
	if err := os.Rename(tmpPath, cleanPath); err != nil {
		return fmt.Errorf("replace compose file: %w", err)
	}

	return nil
}

func rewriteComposeYaml(in io.Reader, out io.Writer, plan model.CpuPlan) error {
	if !plan.HasCpus() {
		_, err := io.Copy(out, in)
		return err
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read compose file: %w", err)
	}

	var compose composeFile
	if err := yamlv3.Unmarshal(data, &compose); err != nil {
		return fmt.Errorf("parse compose yaml: %w", err)
	}

	if err := compose.validate(); err != nil {
		return fmt.Errorf("validate compose yaml: %w", err)
	}

	cpuset := plan.CpuSet()
	// Assign the cpu plan to all services in the compose file
	for name, service := range compose.Services {
		service.Cpuset = cpuset
		if err := setEnvironmentVarNode(&service.Environment, ComposeCpuSetEnvVarName, cpuset); err != nil {
			return fmt.Errorf("set cpuset environment variable for service %q: %w", name, err)
		}
		compose.Services[name] = service
	}

	encoder := yamlv3.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&compose); err != nil {
		return fmt.Errorf("encode patched compose yaml: %w", err)
	}

	return encoder.Close()
}

func setEnvironmentVarNode(env *yamlv3.Node, key string, value string) error {
	if env.Kind == 0 || env.Tag == "!!null" {
		env.Kind = yamlv3.MappingNode
		env.Tag = "!!map"
		env.Value = ""
		env.Content = nil
	}

	switch env.Kind {
	case yamlv3.MappingNode:
		for i := 0; i+1 < len(env.Content); i += 2 {
			if env.Content[i].Value == key {
				env.Content[i+1].Kind = yamlv3.ScalarNode
				env.Content[i+1].Tag = "!!str"
				env.Content[i+1].Value = value
				return nil
			}
		}
		env.Content = append(env.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: key},
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value},
		)
		return nil
	case yamlv3.SequenceNode:
		prefix := key + "="
		entry := prefix + value
		for _, item := range env.Content {
			if item.Kind == yamlv3.ScalarNode && strings.HasPrefix(item.Value, prefix) {
				item.Tag = "!!str"
				item.Value = entry
				return nil
			}
		}
		env.Content = append(env.Content, &yamlv3.Node{
			Kind:  yamlv3.ScalarNode,
			Tag:   "!!str",
			Value: entry,
		})
		return nil
	default:
		return fmt.Errorf("unsupported environment node kind: %d", env.Kind)
	}
}
