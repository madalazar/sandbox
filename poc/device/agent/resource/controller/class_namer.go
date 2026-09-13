package controller

import (
	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

// spells a slot the ledger has confirmed is free
type ClassNamer interface {
	Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error)
}
