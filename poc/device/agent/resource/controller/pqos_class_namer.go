package controller

import (
	"strconv"

	"github.com/margo/sandbox/poc/device/agent/resource/model"
)

var _ ClassNamer = (*PqosClassNamer)(nil)

// names classes of service for pqos by selecting the lowest free integer cos
type PqosClassNamer struct{}

func NewPqosClassNamer() *PqosClassNamer {
	return &PqosClassNamer{}
}

// selects the lowest free class of service index as decimal string (skipping cos0)
func (n *PqosClassNamer) Name(ref model.ComponentRef, taken []model.ClosId) (model.ClosId, error) {
	used := make(map[int]struct{}, len(taken))
	for _, c := range taken {
		if idx, err := strconv.Atoi(string(c)); err == nil {
			used[idx] = struct{}{}
		}
	}

	for cos := 1; ; cos++ {
		if _, found := used[cos]; !found {
			return model.ClosId(strconv.Itoa(cos)), nil
		}
	}
}
