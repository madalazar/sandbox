package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	yamlv3 "gopkg.in/yaml.v3"
)

// ComposeConfigurator applies a CPU plan to a Compose package by rewriting the
// downloaded file into a temporary copy it owns.
type ComposeConfigurator struct{}

func NewComposeConfigurator() *ComposeConfigurator {
	return &ComposeConfigurator{}
}

// Apply writes the plan's cpuset into the source file's single service and returns the
// prepared path.
func (c *ComposeConfigurator) Apply(
	plan CpuPlan,
	owner OwnerRef,
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

func rewriteComposeFile(sourcePath string, targetPath string, plan CpuPlan) error {
	in, err := os.Open(filepath.Clean(sourcePath))
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(filepath.Clean(targetPath))
	if err != nil {
		return err
	}

	rewriteErr := RewriteComposeYAML(in, out, plan)
	closeErr := out.Close()
	if rewriteErr != nil {
		return rewriteErr
	}

	return closeErr
}

func RewriteComposeYAML(in io.Reader, out io.Writer, plan CpuPlan) error {
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

	serviceName, serviceNode, err := composeSingleServiceNode(&root)
	if err != nil {
		return err
	}

	cpuset := plan.CpuSet()
	if err := setServiceCpuset(serviceNode, cpuset); err != nil {
		return fmt.Errorf("set cpuset for service %q: %w", serviceName, err)
	}

	if err := setServiceEnvironmentVariable(serviceNode, "TEST_CPUSET", cpuset); err != nil {
		return fmt.Errorf("set TEST_CPUSET environment variable for service %q: %w", serviceName, err)
	}

	encoder := yamlv3.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("encode patched compose yaml: %w", err)
	}

	return nil
}

// composeSingleServiceNode returns the file's only service. A package deploys exactly one
// unit, so the plan binds positionally and no service name is derived from the component.
func composeSingleServiceNode(root *yamlv3.Node) (string, *yamlv3.Node, error) {
	if root == nil || len(root.Content) == 0 {
		return "", nil, fmt.Errorf("compose yaml is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yamlv3.MappingNode {
		return "", nil, fmt.Errorf("compose yaml root must be a mapping")
	}

	services := mappingValueByKey(doc, "services")
	if services == nil || services.Kind != yamlv3.MappingNode {
		return "", nil, fmt.Errorf("compose yaml must declare a services mapping")
	}

	if count := len(services.Content) / 2; count != 1 {
		return "", nil, fmt.Errorf("compose yaml must declare exactly one service, found %d", count)
	}

	return services.Content[0].Value, services.Content[1], nil
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
