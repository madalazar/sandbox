package configurator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
	yamlv3 "gopkg.in/yaml.v3"
)

const ComposeCpuSetEnvVarName = "TEST_CPUSET"

var fileTokenReplacer = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// applies a cpu plan to a compose package by rewriting the downloaded file into a
// temporary copy it owns
type ComposeConfigurator struct{}

func NewComposeConfigurator() *ComposeConfigurator {
	return &ComposeConfigurator{}
}

// writes the plan's cpuset into the services of the source file and returns the path of
// the prepared copy. A plan with no cpus returns sourcePath unchanged, so the caller
// must not assume it owns the returned file
func (c *ComposeConfigurator) Apply(
	plan model.CpuPlan,
	owner model.OwnerRef,
	sourcePath string,
) (preparedPath string, err error) {
	if !plan.HasCpus() {
		return sourcePath, nil
	}

	file, err := os.CreateTemp("", fmt.Sprintf(
		"compose-pinned-%s-%s-*.yaml",
		sanitizeFileToken(string(owner.Component)),
		sanitizeFileToken(owner.Deployment),
	))
	if err != nil {
		return "", fmt.Errorf("create pinned compose file: %w", err)
	}

	preparedPath = filepath.Clean(file.Name())

	if err := file.Close(); err != nil {
		removePreparedComposeFile(preparedPath)
		return "", fmt.Errorf("close pinned compose file: %w", err)
	}

	if err := rewriteComposeFile(sourcePath, preparedPath, plan); err != nil {
		removePreparedComposeFile(preparedPath)
		return "", fmt.Errorf("rewrite compose yaml: %w", err)
	}

	return preparedPath, nil
}

func removePreparedComposeFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Println("Failed to remove temporary pinned compose file:", path, err)
	}
}

func rewriteComposeFile(sourcePath string, targetPath string, plan model.CpuPlan) error {
	in, err := os.Open(filepath.Clean(sourcePath))
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(filepath.Clean(targetPath))
	if err != nil {
		return err
	}

	rewriteErr := rewriteComposeYaml(in, out, plan)
	closeErr := out.Close()
	if rewriteErr != nil {
		return rewriteErr
	}

	return closeErr
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

	var root yamlv3.Node
	if err := yamlv3.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse compose yaml: %w", err)
	}

	serviceNodes, err := getComposeServiceNodes(&root)
	if err != nil {
		return err
	}

	cpuset := plan.CpuSet()

	for serviceName, serviceNode := range serviceNodes {
		if err := setServiceCpuset(serviceNode, cpuset); err != nil {
			return fmt.Errorf("set cpuset for service %q: %w", serviceName, err)
		}

		if err := setServiceEnvironmentVariable(serviceNode, ComposeCpuSetEnvVarName, cpuset); err != nil {
			return fmt.Errorf("set cpuset environment variable for service %q: %w", serviceName, err)
		}
	}

	encoder := yamlv3.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("encode patched compose yaml: %w", err)
	}

	return nil
}

// IMPORTANT: we can't safely assume that we will have only one compose service inside a yaml
// file so we will assume that we want to assign the cpu plan to all the services inside the file
func getComposeServiceNodes(root *yamlv3.Node) (map[string]*yamlv3.Node, error) {
	if root == nil || len(root.Content) == 0 {
		return nil, fmt.Errorf("compose yaml is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose yaml root must be a mapping")
	}

	services := mappingValueByKey(doc, "services")
	if services == nil || services.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose yaml must declare a services mapping")
	}

	result := make(map[string]*yamlv3.Node)
	for i := 0; i+1 < len(services.Content); i += 2 {
		nameNode := services.Content[i]
		valueNode := services.Content[i+1]
		result[nameNode.Value] = valueNode
	}
	return result, nil
}

func mappingValueByKey(mapping *yamlv3.Node, key string) *yamlv3.Node {
	if mapping == nil || mapping.Kind != yamlv3.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setOrAppendMappingScalar(mapping *yamlv3.Node, key string, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1].Kind = yamlv3.ScalarNode
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Value = value
			return
		}
	}

	mapping.Content = append(mapping.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: key},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: value},
	)
}

func setServiceCpuset(serviceNode *yamlv3.Node, cpuset string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	setOrAppendMappingScalar(serviceNode, "cpuset", cpuset)
	return nil
}

func setServiceEnvironmentVariable(serviceNode *yamlv3.Node, varName string, varValue string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	envNode := mappingValueByKey(serviceNode, "environment")
	if envNode == nil {
		envNode = &yamlv3.Node{
			Kind:    yamlv3.MappingNode,
			Tag:     "!!map",
			Content: []*yamlv3.Node{},
		}
		serviceNode.Content = append(serviceNode.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "environment"},
			envNode,
		)
	}

	if envNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service environment must be a mapping")
	}

	setOrAppendMappingScalar(envNode, varName, varValue)
	return nil
}

func sanitizeFileToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	cleaned := fileTokenReplacer.ReplaceAllString(value, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}
