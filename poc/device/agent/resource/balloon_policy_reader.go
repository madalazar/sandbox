package resource

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var balloonsPolicyGVR = schema.GroupVersionResource{
	Group:    "config.nri",
	Version:  "v1alpha1",
	Resource: model.BalloonsPolicyResource,
}

var _ model.BalloonPolicyReader = (*BalloonPolicyInformer)(nil)

// watches the cluster's BalloonsPolicy custom resource and keeps the latest parsed
// snapshot so planners can read it without blocking on the api server
type BalloonPolicyInformer struct {
	log      *zap.SugaredLogger
	factory  dynamicinformer.DynamicSharedInformerFactory
	informer cache.SharedIndexInformer
	stopCh   chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	// for the poc we expect only one nri balloon resource policy to be stored in the cache
	cache atomic.Pointer[model.ParsedBalloonPolicy]
}

type rawBalloonTypesConfig struct {
	BalloonTypes []model.ParsedBalloonType `json:"balloonTypes"`
}

type rawPolicyControl struct {
	Control *rawControlConfig `json:"control"`
}

type rawControlConfig struct {
	Rdt *rawRdtConfig `json:"rdt"`
}

type rawRdtConfig struct {
	Partitions map[string]rawRdtPartition `json:"partitions"`
}

type rawRdtPartition struct {
	Classes map[string]any `json:"classes"`
}

// builds the informer against the given kubeconfig, or against the in-cluster config
// when the path is empty
func NewBalloonPolicyInformer(kubeconfigPath string, log *zap.SugaredLogger) (*BalloonPolicyInformer, error) {
	restConfig, err := buildKubeRestConfig(kubeconfigPath)
	if err != nil {
		return nil, err
	}

	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client: %w", err)
	}

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
		dynClient,
		0,
		model.DefaultBalloonsPolicyNamespace,
		nil,
	)
	informer := factory.ForResource(balloonsPolicyGVR).Informer()

	b := &BalloonPolicyInformer{
		log:      log,
		factory:  factory,
		informer: informer,
		stopCh:   make(chan struct{}),
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			b.selectAndParse()
		},
		UpdateFunc: func(oldObj, newObj any) {
			b.selectAndParse()
		},
		DeleteFunc: func(obj any) {
			// once we decide to suppport multiple nri resource policies this needs to be updated
			// as we clear the cache completely when a policy is deleted
			b.cache.Store(nil)
			b.log.Infow("nri balloons resource policy deleted; cleared local policy cache")
		},
	})

	return b, nil
}

// begins watching and blocks until the informer cache has synced once
// callers should supply a context with an explicit timeout during the agent's Start()
// so that cache sync does not block indefinitely if the CRD is absent or API server is unreachable
func (b *BalloonPolicyInformer) Start(ctx context.Context) error {
	b.startOnce.Do(func() {
		b.factory.Start(b.stopCh)
	})

	if ok := cache.WaitForCacheSync(ctx.Done(), b.informer.HasSynced); !ok {
		b.Stop()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("timed out waiting for balloons policy informer cache sync: %w", err)
		}
		return fmt.Errorf("timed out waiting for balloons policy informer cache sync")
	}

	b.selectAndParse()
	return nil
}

// stops the watch; safe to call more than once
//
//	will be called from the agent's Stop() function in main.go to cleanly terminate the watch
func (b *BalloonPolicyInformer) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
}

// returns an immutable snapshot (deep copy) of the latest policy,
// or nil when no policy is present
// callers cannot mutate the cached policy
func (b *BalloonPolicyInformer) Parsed() *model.ParsedBalloonPolicy {
	return b.cache.Load().DeepCopy()
}

func (b *BalloonPolicyInformer) selectAndParse() {
	items := b.informer.GetStore().List()
	if len(items) == 0 {
		b.cache.Store(nil)
		b.log.Infow("no nri resource balloon crs found; continuing without NRI annotations")
		return
	}
	if len(items) > 1 {
		b.log.Warnw("multiple nri resource balloon crs found; using first object", "count", len(items))
	}

	obj, ok := items[0].(*unstructured.Unstructured)
	if !ok {
		b.cache.Store(nil)
		b.log.Warnw("Unexpected informer object type for BalloonsPolicy; cache cleared")
		return
	}

	parsed, err := parseBalloonsPolicy(obj)
	if err != nil {
		b.cache.Store(nil)
		b.log.Warnw("failed to parse nri resource balloon policy ; cache cleared", "name", obj.GetName(), "err", err)
		return
	}

	b.cache.Store(parsed)
	b.log.Debugw("updated nri resource balloon policy cache", "name", parsed.Name, "balloonTypes", len(parsed.BalloonTypes))
}

func (r *rawBalloonTypesConfig) validate() error {
	for idx := range r.BalloonTypes {
		bt := &r.BalloonTypes[idx]
		bt.Name = strings.TrimSpace(bt.Name)
		if bt.Name == "" {
			return fmt.Errorf("balloonTypes[%d] has empty or missing name", idx)
		}
		if bt.MinCpus != nil && *bt.MinCpus < 0 {
			return fmt.Errorf("balloonTypes[%d] %q has negative minCPUs: %d", idx, bt.Name, *bt.MinCpus)
		}
		if bt.MaxCpus != nil && *bt.MaxCpus < 0 {
			return fmt.Errorf("balloonTypes[%d] %q has negative maxCPUs: %d", idx, bt.Name, *bt.MaxCpus)
		}
		if bt.MinCpus != nil && bt.MaxCpus != nil && *bt.MinCpus > *bt.MaxCpus {
			return fmt.Errorf("balloonTypes[%d] %q has minCPUs (%d) greater than maxCPUs (%d)", idx, bt.Name, *bt.MinCpus, *bt.MaxCpus)
		}
	}
	return nil
}

func parseBalloonsPolicy(obj *unstructured.Unstructured) (*model.ParsedBalloonPolicy, error) {
	if obj == nil {
		return nil, fmt.Errorf("nil policy object")
	}

	cfg := extractPolicyConfig(obj.Object)
	if cfg == nil {
		return nil, fmt.Errorf("policy config section not found")
	}

	var raw rawBalloonTypesConfig
	if rawBalloons, exists := cfg[model.PolicyKeyBalloonTypes]; exists && rawBalloons != nil {
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			map[string]any{model.PolicyKeyBalloonTypes: rawBalloons},
			&raw,
		); err != nil {
			return nil, fmt.Errorf("failed to decode policy config: %w", err)
		}
	}

	if err := raw.validate(); err != nil {
		return nil, err
	}

	rdtConfig := model.RdtConfig{
		Partitions: map[string]struct{}{},
		Classes:    map[string]struct{}{},
	}
	if rawCtrl, exists := cfg[model.PolicyKeyControl]; exists && rawCtrl != nil {
		var ctrl rawPolicyControl
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			map[string]any{model.PolicyKeyControl: rawCtrl},
			&ctrl,
		); err == nil && ctrl.Control != nil && ctrl.Control.Rdt != nil {
			for partitionName, partition := range ctrl.Control.Rdt.Partitions {
				rdtConfig.Partitions[partitionName] = struct{}{}
				for className := range partition.Classes {
					rdtConfig.Classes[className] = struct{}{}
				}
			}
		}
	}

	return &model.ParsedBalloonPolicy{
		Name:         obj.GetName(),
		Namespace:    obj.GetNamespace(),
		BalloonTypes: raw.BalloonTypes,
		RdtConfig:    rdtConfig,
	}, nil
}

func extractPolicyConfig(root map[string]any) map[string]any {
	if root == nil {
		return nil
	}

	if cfg, ok := root[model.PolicyKeyConfig].(map[string]any); ok {
		return cfg
	}

	spec, ok := root[model.PolicyKeySpec].(map[string]any)
	if !ok {
		return nil
	}

	if cfg, ok := spec[model.PolicyKeyConfig].(map[string]any); ok {
		return cfg
	}
	if _, ok := spec[model.PolicyKeyBalloonTypes]; ok {
		return spec
	}
	if _, ok := spec[model.PolicyKeyControl]; ok {
		return spec
	}

	return nil
}

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

	// Fallback to default kubeconfig loading for local/dev runs.
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	clientCfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	fallbackCfg, fallbackErr := clientCfg.ClientConfig()
	if fallbackErr != nil {
		return nil, fmt.Errorf("failed to build in-cluster or default kube config: in-cluster err: %w; default config err: %w", err, fallbackErr)
	}
	return fallbackCfg, nil
}
