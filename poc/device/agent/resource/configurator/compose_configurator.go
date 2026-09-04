package configurator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/margo/sandbox/poc/device/agent/resource"
	"github.com/margo/sandbox/poc/device/agent/resource/model"
	yamlv3 "gopkg.in/yaml.v3"
)

const ComposeCpuSetEnvVarName = "TEST_CPUSET"

// applies a cpu plan to a compose package by rewriting the downloaded file into a
// temporary copy it owns
type ComposeConfigurator struct{}

func NewComposeConfigurator() *ComposeConfigurator {
	return &ComposeConfigurator{}
}

// writes the plan's cpuset into the source file's single service and returns the path of
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
		resource.SanitizeFileToken(string(owner.Component)),
		resource.SanitizeFileToken(owner.Deployment),
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

	rewriteErr := RewriteComposeYaml(in, out, plan)
	closeErr := out.Close()
	if rewriteErr != nil {
		return rewriteErr
	}

	return closeErr
}

func RewriteComposeYaml(in io.Reader, out io.Writer, plan model.CpuPlan) error {
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

	serviceNodes, err := updateComposeServiceNodes(&root)
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
func updateComposeServiceNodes(root *yamlv3.Node) (map[string]*yamlv3.Node, error) {
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

	if services.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose services must be a mapping")
	}

	result := make(map[string]*yamlv3.Node)
	for i := 0; i+1 < len(services.Content); i += 2 {
		nameNode := services.Content[i]
		valueNode := services.Content[i+1]
		// TODO: hardcoding the "_compose" suffix here
		// to deal with multiple services inside the docker-compose.yaml
		// file for the same component. Once we decide there
		// will be only one component per compose file, we can remove this suffix.
		// or we add another field to map component to service name
		result[nameNode.Value+"_compose"] = valueNode
		// TODO: investigate why the need for the suffix _compose
		// result[nameNode.Value] = valueNode
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

func setServiceCpuset(serviceNode *yamlv3.Node, cpuset string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	for i := 0; i+1 < len(serviceNode.Content); i += 2 {
		if serviceNode.Content[i].Value == "cpuset" {
			serviceNode.Content[i+1].Kind = yamlv3.ScalarNode
			serviceNode.Content[i+1].Tag = "!!str"
			serviceNode.Content[i+1].Value = cpuset
			return nil
		}
	}

	serviceNode.Content = append(serviceNode.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "cpuset"},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: cpuset},
	)
	return nil
}

func setServiceEnvironmentVariable(serviceNode *yamlv3.Node, varName string, varValue string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	var envNode *yamlv3.Node
	for i := 0; i+1 < len(serviceNode.Content); i += 2 {
		if serviceNode.Content[i].Value == "environment" {
			envNode = serviceNode.Content[i+1]
			break
		}
	}

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

	for i := 0; i+1 < len(envNode.Content); i += 2 {
		if envNode.Content[i].Value == varName {
			envNode.Content[i+1].Kind = yamlv3.ScalarNode
			envNode.Content[i+1].Tag = "!!str"
			envNode.Content[i+1].Value = varValue
			return nil
		}
	}

	envNode.Content = append(envNode.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varName},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varValue},
	)
	return nil
}
