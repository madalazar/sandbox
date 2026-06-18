package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	yamlv3 "gopkg.in/yaml.v3"
)

type CpuAssignment struct {
	Requirement string
	Cpus        []int
}

func (dm *DeploymentManager) resolveComponentCpuAssignments(deploymentID string, componentName string, composeFilePath string,
	requiredResources *sbi.RequiredResources, existingAssignments map[string][]int) ([]CpuAssignment, error) {
	if requiredResources == nil || requiredResources.Cpu == nil || len(*requiredResources.Cpu) == 0 {
		return nil, nil
	}

	if len(dm.topologyLookup.IsolatedCPUIndices) == 0 {
		return nil, nil
	}

	// Filter to only the CPU requirements that belong to this component.
	componentCpuReqs := make([]sbi.Cpu, 0)
	for _, req := range *requiredResources.Cpu {
		if req.Name != nil && strings.EqualFold(strings.TrimSpace(*req.Name), componentName) {
			componentCpuReqs = append(componentCpuReqs, req)
		}
	}

	isIsolatedRequest := false
	for _, req := range componentCpuReqs {
		if req.Type != nil && *req.Type == sbi.CpuTypeIsolated {
			isIsolatedRequest = true
			break
		}
	}
	if !isIsolatedRequest {
		return nil, nil
	}

	serviceNames, err := composeServiceNames(composeFilePath)
	if err != nil {
		return nil, fmt.Errorf("read compose services for component %q: %w", componentName, err)
	}

	reqs := make([]sbi.Cpu, 0, len(componentCpuReqs))
	for _, req := range componentCpuReqs {
		if req.Type == nil || *req.Type != sbi.CpuTypeIsolated {
			continue
		}
		if req.Name == nil {
			return nil, fmt.Errorf("isolated CPU requirement is missing name")
		}

		requirementName := strings.TrimSpace(*req.Name)
		if requirementName == "" {
			return nil, fmt.Errorf("isolated CPU requirement has empty name")
		}
		if _, exists := serviceNames[requirementName]; !exists {
			return nil, fmt.Errorf("isolated CPU requirement %q does not match a compose service in component %q", requirementName, componentName)
		}

		reqs = append(reqs, req)
	}

	if len(reqs) == 0 {
		return nil, nil
	}

	taken := dm.database.AllocatedCpus()
	for requirement, cpuIndices := range existingAssignments {
		owner := deploymentID
		if strings.TrimSpace(requirement) != "" {
			owner = deploymentID + "/" + strings.TrimSpace(requirement)
		}

		for _, cpuIndex := range cpuIndices {
			if _, exists := dm.topologyLookup.IsolatedCPUSet[cpuIndex]; !exists {
				continue
			}
			taken[cpuIndex] = owner
		}
	}

	assignments := make([]CpuAssignment, 0, len(reqs))

	for _, req := range reqs {
		requirementName := strings.TrimSpace(*req.Name)
		expectedOwner := deploymentID + "/" + requirementName

		requiredCores := int64(1)
		if req.Cores != nil && *req.Cores > 0 {
			requiredCores = int64(math.Ceil(float64(*req.Cores)))
		}

		candidates := dm.topologyLookup.IsolatedCPUIndices
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no isolated CPUs available for requirement %q", requirementName)
		}

		selected := make([]int, 0, requiredCores)
		for _, cpu := range candidates {
			owner := taken[cpu]
			if owner != "" && owner != deploymentID && owner != expectedOwner {
				continue
			}
			selected = append(selected, cpu)
			if len(selected) == int(requiredCores) {
				break
			}
		}

		if len(selected) < int(requiredCores) {
			return nil, fmt.Errorf("no free isolated CPUs available for requirement %q (required=%d)", requirementName, requiredCores)
		}

		for _, cpu := range selected {
			taken[cpu] = deploymentID + "/" + requirementName
		}

		assignments = append(assignments, CpuAssignment{
			Requirement: requirementName,
			Cpus:        selected,
		})
	}

	return assignments, nil
}

func composeServiceNames(composePath string) (map[string]struct{}, error) {
	content, err := os.ReadFile(composePath)
	if err != nil {
		return nil, err
	}

	var root map[string]interface{}
	if err := yamlv3.Unmarshal(content, &root); err != nil {
		return nil, err
	}

	serviceNames := map[string]struct{}{}
	rawServices, exists := root["services"]
	if !exists {
		return serviceNames, nil
	}

	services, ok := rawServices.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("compose services is not a map")
	}

	for name := range services {
		serviceNames[name] = struct{}{}
	}

	return serviceNames, nil
}

func rewriteComposeFile(sourcePath string, targetPath string, assignments []CpuAssignment) error {
	in, err := os.Open(filepath.Clean(sourcePath))
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(filepath.Clean(targetPath))
	if err != nil {
		return err
	}

	rewriteErr := RewriteComposeYAML(in, out, assignments)
	closeErr := out.Close()
	if rewriteErr != nil {
		return rewriteErr
	}
	if closeErr != nil {
		return closeErr
	}

	return nil
}

func RewriteComposeYAML(in io.Reader, out io.Writer, assignments []CpuAssignment) error {
	if len(assignments) == 0 {
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

	assignmentByService := toAssignmentMap(assignments)
	for serviceName, cpus := range assignmentByService {
		serviceNode, exists := serviceMap[serviceName]
		if !exists {
			return fmt.Errorf("assignment references unknown compose service %q", serviceName)
		}

		cpuset := formatCPUSet(cpus)
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

func toAssignmentMap(assignments []CpuAssignment) map[string][]int {
	result := make(map[string][]int, len(assignments))
	for _, assignment := range assignments {
		copied := make([]int, len(assignment.Cpus))
		copy(copied, assignment.Cpus)
		result[assignment.Requirement] = copied
	}
	return result
}

func formatCPUSet(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}

	sorted := make([]int, len(cpus))
	copy(sorted, cpus)
	sort.Ints(sorted)

	compact := make([]string, 0, len(sorted))
	start := sorted[0]
	prev := sorted[0]

	flush := func(s int, e int) {
		if s == e {
			compact = append(compact, strconv.Itoa(s))
			return
		}
		compact = append(compact, fmt.Sprintf("%d-%d", s, e))
	}

	for i := 1; i < len(sorted); i++ {
		current := sorted[i]
		if current == prev+1 {
			prev = current
			continue
		}
		flush(start, prev)
		start = current
		prev = current
	}
	flush(start, prev)

	return strings.Join(compact, ",")
}
