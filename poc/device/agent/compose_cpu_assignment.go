package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	yamlv3 "gopkg.in/yaml.v3"
)

func (dm *DeploymentManager) resolveComponentCpuAssignments(
	componentName string,
	requiredResources *sbi.RequiredResources,
	ledger *AllocationLedger,
) (CpuPlan, error) {
	requirements, err := NormalizeCPURequirements(ComponentRef(componentName), requiredResources)
	if err != nil {
		return CpuPlan{}, err
	}

	if !requirements.HasIsolatedCores() || len(dm.topologyLookup.IsolatedCPUIndices) == 0 {
		return CpuPlan{}, nil
	}

	return selectIsolatedCPUs(dm.topologyLookup.IsolatedCPUIndices, ledger, requirements)
}

// selectIsolatedCPUs picks the isolated CPU indices each requirement gets and reserves
// them in the ledger, so requirements planned in the same pass cannot select the same
// index twice.
func selectIsolatedCPUs(
	isolated []int,
	ledger *AllocationLedger,
	requirements NormalizedCPURequirements,
) (CpuPlan, error) {
	plan := CpuPlan{Assignments: make([]CpuAssignment, 0, len(requirements.Isolated))}

	for _, requirement := range requirements.Isolated {
		if len(isolated) == 0 {
			return CpuPlan{}, fmt.Errorf("no isolated CPUs available for component %q", requirements.Component)
		}

		selected := make([]int, 0, requirement.Cores)
		for _, cpu := range isolated {
			if !ledger.IsCpuAvailable(cpu, requirements.Component) {
				continue
			}
			selected = append(selected, cpu)
			if len(selected) == requirement.Cores {
				break
			}
		}

		if len(selected) < requirement.Cores {
			return CpuPlan{}, fmt.Errorf(
				"no free isolated CPUs available for component %q (required=%d)",
				requirements.Component, requirement.Cores,
			)
		}

		if err := ledger.ReserveCPUs(requirements.Component, selected); err != nil {
			return CpuPlan{}, err
		}

		plan.Assignments = append(plan.Assignments, CpuAssignment{
			Component: requirements.Component,
			Cpus:      selected,
		})
	}

	return plan, nil
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
	if closeErr != nil {
		return closeErr
	}

	return nil
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

	serviceMap, err := composeServicesNode(&root)
	if err != nil {
		return err
	}

	// Keyed by requirement name; composeServicesNode spells services to match until the
	// single-service binding lands.
	for serviceName, cpus := range plan.AssignmentMap() {
		serviceNode, exists := serviceMap[serviceName]
		if !exists {
			return fmt.Errorf("assignment references unknown compose service %q", serviceName)
		}

		cpuset := formatCpuSet(cpus)
		if err := setServiceCPuset(serviceNode, cpuset); err != nil {
			return fmt.Errorf("set cpuset for service %q: %w", serviceName, err)
		}

		if err := setServiceEnvironmentVariable(serviceNode, "TEST_CPUSET", cpuset); err != nil {
			return fmt.Errorf("set TEST_CPUSET environment variable for service %q: %w", serviceName, err)
		}
	}

	encoder := yamlv3.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("encode patched compose yaml: %w", err)
	}

	return nil
}

func composeServicesNode(root *yamlv3.Node) (map[string]*yamlv3.Node, error) {
	if root == nil || len(root.Content) == 0 {
		return nil, fmt.Errorf("compose yaml is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose yaml root must be a mapping")
	}

	services := mappingValueByKey(doc, "services")
	if services == nil {
		return map[string]*yamlv3.Node{}, nil
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

func setServiceCPuset(serviceNode *yamlv3.Node, cpuset string) error {
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

	// Find or create the environment section
	var envNode *yamlv3.Node
	for i := 0; i+1 < len(serviceNode.Content); i += 2 {
		if serviceNode.Content[i].Value == "environment" {
			envNode = serviceNode.Content[i+1]
			break
		}
	}

	// If environment doesn't exist, create it
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

	// Look for existing variable
	for i := 0; i+1 < len(envNode.Content); i += 2 {
		if envNode.Content[i].Value == varName {
			envNode.Content[i+1].Kind = yamlv3.ScalarNode
			envNode.Content[i+1].Tag = "!!str"
			envNode.Content[i+1].Value = varValue
			return nil
		}
	}

	// If not found, add it
	envNode.Content = append(envNode.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varName},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varValue},
	)
	return nil
}
