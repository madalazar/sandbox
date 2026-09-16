package workloads

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// creates a kubernetes *rest.Config with fallback to in-cluster or default rules
func buildKubeRestConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build kube config from %q: %w", kubeconfigPath, err)
		}
		return cfg, nil
	}

	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}

	// fallback to default kubeconfig loading for local/dev runs
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	fallbackCfg, fallbackErr := clientCfg.ClientConfig()
	if fallbackErr != nil {
		return nil, fmt.Errorf("failed to build in-cluster or default kube config: in-cluster err: %w; default config err: %w", err, fallbackErr)
	}
	return fallbackCfg, nil
}

// creates a kubernetes dynamic client for the given kubeconfig path
func NewDynamicClient(kubeconfigPath string) (dynamic.Interface, error) {
	restConfig, err := buildKubeRestConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(restConfig)
}

// creates a kubernetes clientset for the given kubeconfig path
func newKubeClient(kubeconfigPath string) (kubernetes.Interface, error) {
	restConfig, err := buildKubeRestConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(restConfig)
}
