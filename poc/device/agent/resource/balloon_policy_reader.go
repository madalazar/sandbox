package resource

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
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

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

const (
	balloonsPolicyNamespace = "kube-system"
)

var balloonsPolicyGVR = schema.GroupVersionResource{
	Group:    "config.nri",
	Version:  "v1alpha1",
	Resource: "balloonspolicies",
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
		balloonsPolicyNamespace,
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
			if b.log != nil {
				b.log.Infow("nri balloons resource policy deleted; cleared local policy cache")
			}
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
	return cloneParsedBalloonPolicy(b.cache.Load())
}

func (b *BalloonPolicyInformer) selectAndParse() {
	items := b.informer.GetStore().List()
	if len(items) == 0 {
		b.cache.Store(nil)
		if b.log != nil {
			b.log.Infow("No BalloonsPolicy CR found; continuing without NRI annotations")
		}
		return
	}
	if len(items) > 1 && b.log != nil {
		b.log.Warnw("Multiple BalloonsPolicy CRs found; using first object", "count", len(items))
	}

	obj, ok := items[0].(*unstructured.Unstructured)
	if !ok {
		b.cache.Store(nil)
		if b.log != nil {
			b.log.Warnw("Unexpected informer object type for BalloonsPolicy; cache cleared")
		}
		return
	}

	parsed, err := parseBalloonsPolicy(obj)
	if err != nil {
		b.cache.Store(nil)
		if b.log != nil {
			b.log.Warnw("Failed to parse BalloonsPolicy; cache cleared", "name", obj.GetName(), "err", err)
		}
		return
	}

	b.cache.Store(parsed)
	if b.log != nil {
		b.log.Debugw("Updated BalloonsPolicy cache", "name", parsed.Name, "balloonTypes", len(parsed.BalloonTypes))
	}
}

// TODO: investigate if there are wasys of cloning an object without writing own method
func cloneParsedBalloonPolicy(p *model.ParsedBalloonPolicy) *model.ParsedBalloonPolicy {
	if p == nil {
		return nil
	}
	out := &model.ParsedBalloonPolicy{
		Name:      p.Name,
		Namespace: p.Namespace,
	}
	if p.BalloonTypes != nil {
		out.BalloonTypes = make([]model.ParsedBalloonType, len(p.BalloonTypes))
		for i, bt := range p.BalloonTypes {
			clonedBt := model.ParsedBalloonType{
				Name:           bt.Name,
				PreferCoreType: bt.PreferCoreType,
			}
			if bt.PreferIsolCpus != nil {
				v := *bt.PreferIsolCpus
				clonedBt.PreferIsolCpus = &v
			}
			if bt.MinCpus != nil {
				v := *bt.MinCpus
				clonedBt.MinCpus = &v
			}
			if bt.MaxCpus != nil {
				v := *bt.MaxCpus
				clonedBt.MaxCpus = &v
			}
			if bt.PreferCloseToDevices != nil {
				clonedBt.PreferCloseToDevices = make([]string, len(bt.PreferCloseToDevices))
				copy(clonedBt.PreferCloseToDevices, bt.PreferCloseToDevices)
			}
			out.BalloonTypes[i] = clonedBt
		}
	}
	return out
}

func parseBalloonsPolicy(obj *unstructured.Unstructured) (*model.ParsedBalloonPolicy, error) {
	if obj == nil {
		return nil, fmt.Errorf("nil policy object")
	}

	cfg := extractPolicyConfig(obj.Object)
	if cfg == nil {
		return nil, fmt.Errorf("policy config section not found")
	}

	out := &model.ParsedBalloonPolicy{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}

	if rawTypes, ok := cfg["balloonTypes"].([]any); ok {
		out.BalloonTypes = make([]model.ParsedBalloonType, 0, len(rawTypes))
		for idx, item := range rawTypes {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("balloonTypes[%d] is not a map: %T", idx, item)
			}

			p := model.ParsedBalloonType{}
			if v, ok := m["name"].(string); ok {
				p.Name = strings.TrimSpace(v)
			}
			if p.Name == "" {
				return nil, fmt.Errorf("balloonTypes[%d] has empty or missing name", idx)
			}

			if v, ok := m["preferCoreType"].(string); ok {
				p.PreferCoreType = v
			}
			if v, ok := m["preferIsolCpus"].(bool); ok {
				vCopy := v
				p.PreferIsolCpus = &vCopy
			}
			var err error
			if p.MinCpus, err = parseNonNegativeCPUField(m, "minCPUs", p.Name, idx); err != nil {
				return nil, err
			}
			if p.MaxCpus, err = parseNonNegativeCPUField(m, "maxCPUs", p.Name, idx); err != nil {
				return nil, err
			}
			if p.MinCpus != nil && p.MaxCpus != nil && *p.MinCpus > *p.MaxCpus {
				return nil, fmt.Errorf("balloonTypes[%d] %q has minCPUs (%d) greater than maxCPUs (%d)", idx, p.Name, *p.MinCpus, *p.MaxCpus)
			}
			if arr, ok := m["preferCloseToDevices"].([]any); ok {
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

func parseNonNegativeCPUField(m map[string]any, fieldName, balloonName string, idx int) (*int64, error) {
	raw, exists := m[fieldName]
	if !exists {
		return nil, nil
	}
	val, ok := asInt64(raw)
	if !ok {
		return nil, fmt.Errorf("balloonTypes[%d] %q has invalid %s value: %v", idx, balloonName, fieldName, raw)
	}
	if val < 0 {
		return nil, fmt.Errorf("balloonTypes[%d] %q has negative %s: %d", idx, balloonName, fieldName, val)
	}
	return &val, nil
}

func extractPolicyConfig(root map[string]any) map[string]any {
	if root == nil {
		return nil
	}

	if cfg, ok := root["config"].(map[string]any); ok {
		return cfg
	}

	spec, ok := root["spec"].(map[string]any)
	if !ok {
		return nil
	}

	if cfg, ok := spec["config"].(map[string]any); ok {
		return cfg
	}
	if _, ok := spec["balloonTypes"]; ok {
		return spec
	}

	return nil
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n <= math.MaxInt64 {
			return int64(n), true
		}
	case float32:
		if float32(int64(n)) == n {
			return int64(n), true
		}
	case float64:
		if float64(int64(n)) == n {
			return int64(n), true
		}
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
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
