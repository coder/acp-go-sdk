//go:build goexperiment.jsonv2

package acp

// With GOEXPERIMENT=jsonv2, encoding/json decodes through encoding/json/v2,
// whose rules the fast decoders do not follow, so only the exact decoders run.
func init() { fastDecode = false }
