package resource

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// marks a contract whose body lands in a later commit
var errNotImplemented = errors.New("not implemented")

var _ model.BalloonPolicyReader = (*BalloonPolicyInformer)(nil)

// watches the cluster's BalloonsPolicy custom resource and keeps the latest parsed
// snapshot so planners can read it without blocking on the api server
type BalloonPolicyInformer struct {
	log *zap.SugaredLogger
}

// builds the informer against the given kubeconfig, or against the in-cluster config
// when the path is empty
func NewBalloonPolicyInformer(kubeconfigPath string, log *zap.SugaredLogger) (*BalloonPolicyInformer, error) {
	return nil, errNotImplemented
}

// begins watching and blocks until the informer cache has synced once
func (b *BalloonPolicyInformer) Start(ctx context.Context) error {
	return errNotImplemented
}

// stops the watch. It is safe to call more than once
func (b *BalloonPolicyInformer) Stop() {}

// returns the latest snapshot, or nil when no policy is present
func (b *BalloonPolicyInformer) Parsed() *model.ParsedBalloonPolicy {
	return nil
}
