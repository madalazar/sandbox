package controller

import (
	"github.com/margo/sandbox/poc/device/agent/resource/ledger"
)

// spells a slot the ledger has confirmed is free
// re-exported from the ledger package for runtime configuration
type ClassNamer = ledger.ClassNamer
