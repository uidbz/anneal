//go:build !cgo || !goolm

package gateway

import "fmt"

// newMatrixE2EETransport is the fallback for gateways built without CGO or
// without the goolm tag. E2EE needs mautrix-go's Olm/Megolm machine; the
// plain Matrix transport remains available in every build.
func newMatrixE2EETransport(cfg TransportConfig) (Transport, error) {
	return nil, fmt.Errorf("gateway: matrix %s has encrypted=true, but this gateway was built without E2EE support; rebuild with CGO_ENABLED=1 and -tags goolm", cfg.Name)
}
