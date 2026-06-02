package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"go.uber.org/zap"
)

const (
	balloonsPolicyNamespace = "kube-system"
)

var balloonsPolicyGVR = schema.GroupVersionResource{
	Group:    "config.nri",
	Version:  "v1alpha1",
	Resource: "balloonspolicies",
}

// BalloonPolicyReader exposes non-blocking reads of the latest parsed policy snapshot.
type BalloonPolicyReader interface {
	Parsed() *ParsedBalloonPolicy
}

// ParsedBalloonType contains the balloon fields needed by later scheduling logic.
type ParsedBalloonType struct {
	Name                 string
	PreferCoreType       string
	PreferIsolCpus       *bool
	MinCPUs              *int64
	MaxCPUs              *int64
	PreferCloseToDevices []string
}

// ParsedBalloonPolicy is the in-memory snapshot used by the deployment reconciler.
type ParsedBalloonPolicy struct {
	Name         string
	Namespace    string
	BalloonTypes []ParsedBalloonType
}

type balloonPolicyInformer struct {
	log      *zap.SugaredLogger
	factory  dynamicinformer.DynamicSharedInformerFactory
	informer cache.SharedIndexInformer
	stopCh   chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	cache     atomic.Pointer[ParsedBalloonPolicy]
}

func newBalloonPolicyInformer(kubeconfigPath string, log *zap.SugaredLogger) (*balloonPolicyInformer, error) {
	restConfig, err := buildKubeRESTConfig(kubeconfigPath)
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
		balloonsPolicyNamespace,
		nil,
	)
	informer := factory.ForResource(balloonsPolicyGVR).Informer()

	b := &balloonPolicyInformer{
		log:      log,
		factory:  factory,
		informer: informer,
		stopCh:   make(chan struct{}),
	}

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			b.selectAndParse()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			b.selectAndParse()
		},
		DeleteFunc: func(obj interface{}) {
			b.cache.Store(nil)
			b.log.Infow("BalloonsPolicy deleted; cleared local policy cache")
		},
	})

	return b, nil
}

func (b *balloonPolicyInformer) Start(ctx context.Context) error {
	b.startOnce.Do(func() {
		b.factory.Start(b.stopCh)
	})

	if ok := cache.WaitForCacheSync(ctx.Done(), b.informer.HasSynced); !ok {
		return fmt.Errorf("timed out waiting for balloons policy informer cache sync")
	}

	b.selectAndParse()
	return nil
}

func (b *balloonPolicyInformer) Stop() {
	b.stopOnce.Do(func() {
		close(b.stopCh)
	})
}

func (b *balloonPolicyInformer) Parsed() *ParsedBalloonPolicy {
	return b.cache.Load()
}

func (b *balloonPolicyInformer) selectAndParse() {
	items := b.informer.GetStore().List()
	if len(items) == 0 {
		b.cache.Store(nil)
		b.log.Infow("No BalloonsPolicy CR found; continuing without NRI annotations")
		return
	}
	if len(items) > 1 {
		b.log.Warnw("Multiple BalloonsPolicy CRs found; using first object", "count", len(items))
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
		b.log.Warnw("Failed to parse BalloonsPolicy; cache cleared", "name", obj.GetName(), "err", err)
		return
	}

	b.cache.Store(parsed)
	b.log.Debugw("Updated BalloonsPolicy cache", "name", parsed.Name, "balloonTypes", len(parsed.BalloonTypes))
}

func parseBalloonsPolicy(obj *unstructured.Unstructured) (*ParsedBalloonPolicy, error) {
	if obj == nil {
		return nil, fmt.Errorf("nil policy object")
	}

	cfg := extractPolicyConfig(obj.Object)
	if cfg == nil {
		return nil, fmt.Errorf("policy config section not found")
	}

	out := &ParsedBalloonPolicy{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	if rawTypes, ok := cfg["balloonTypes"].([]interface{}); ok {
		out.BalloonTypes = make([]ParsedBalloonType, 0, len(rawTypes))
		for _, item := range rawTypes {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}

			p := ParsedBalloonType{}
			if v, ok := m["name"].(string); ok {
				p.Name = v
			}
			if v, ok := m["preferCoreType"].(string); ok {
				p.PreferCoreType = v
			}
			if v, ok := m["preferIsolCpus"].(bool); ok {
				vCopy := v
				p.PreferIsolCpus = &vCopy
			}
			if v, ok := asInt64(m["minCPUs"]); ok {
				vCopy := v
				p.MinCPUs = &vCopy
			}
			if v, ok := asInt64(m["maxCPUs"]); ok {
				vCopy := v
				p.MaxCPUs = &vCopy
			}
			if arr, ok := m["preferCloseToDevices"].([]interface{}); ok {
				for _, path := range arr {
					if s, ok := path.(string); ok {
						p.PreferCloseToDevices = append(p.PreferCloseToDevices, s)
					}
				}
			}

			out.BalloonTypes = append(out.BalloonTypes, p)
		}
	}

	return out, nil
}

func extractPolicyConfig(root map[string]interface{}) map[string]interface{} {
	if root == nil {
		return nil
	}

	if cfg, ok := root["config"].(map[string]interface{}); ok {
		return cfg
	}

	spec, ok := root["spec"].(map[string]interface{})
	if !ok {
		return nil
	}

	if cfg, ok := spec["config"].(map[string]interface{}); ok {
		return cfg
	}
	if _, ok := spec["balloonTypes"]; ok {
		return spec
	}

	return nil
}

func asInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float32:
		return int64(n), true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

func buildKubeRESTConfig(kubeconfigPath string) (*rest.Config, error) {
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
		return nil, fmt.Errorf("failed to build in-cluster or default kube config: %w / %v", err, fallbackErr)
	}
	return fallbackCfg, nil
}
