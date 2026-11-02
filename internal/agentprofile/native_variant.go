package agentprofile

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

const nativeVariantPrefix = "@native-variant:"

type nativeVariantSelection struct {
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// NativeVariantRef persists the native model and its opaque backend identifier.
// Execution can reconstruct the selection without a stale built-in catalog.
func NativeVariantRef(model, variant string) string {
	data, _ := json.Marshal(nativeVariantSelection{Model: model, Variant: variant})
	return nativeVariantPrefix + base64.RawURLEncoding.EncodeToString(data)
}
func resolveNativeVariant(ref string) (RuntimeModelOption, error) {
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(ref, nativeVariantPrefix))
	var value nativeVariantSelection
	if err != nil || json.Unmarshal(data, &value) != nil || strings.TrimSpace(value.Model) == "" || strings.TrimSpace(value.Variant) == "" || strings.ContainsAny(value.Model+value.Variant, "\x00\r\n") {
		return RuntimeModelOption{}, profileError("native_variant_invalid")
	}
	return RuntimeModelOption{Ref: ref, Kind: ModelKindNative, AgentModel: value.Model, BackendVariant: value.Variant, DisplayName: value.Model + " (" + value.Variant + ")", NativeCapabilitiesUnknown: true}, nil
}
