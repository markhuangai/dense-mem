package assessor

import "github.com/markhuangai/dense-mem/internal/modelprovider"

// Provider supplies the named structured transport used by Evidence assessment and partitioning.
type Provider interface {
	modelprovider.StructuredTransport
	ModelName() string
}
