#!/bin/bash -eu

compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/config FuzzUnmarshalYAML fuzz_unmarshal_yaml
compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/config FuzzUnmarshalTOML fuzz_unmarshal_toml
compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/config FuzzValidateService fuzz_validate_service
compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/config FuzzLoadWithInheritance fuzz_load_with_inheritance
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/hook FuzzExpandVars fuzz_expand_vars
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/hook FuzzExpandVarsJSON fuzz_expand_vars_json
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/hook FuzzParseEventType fuzz_parse_event_type
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/hook FuzzBuildFromConfig fuzz_build_from_config
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/cli FuzzParseSvcFlag fuzz_parse_svc_flag
compile_native_go_fuzzer github.com/rbaliyan/kubeport/internal/cli FuzzServiceConfigProtoRoundTrip fuzz_service_config_proto_round_trip
compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/proxy FuzzResolveAddr fuzz_resolve_addr
compile_native_go_fuzzer github.com/rbaliyan/kubeport/pkg/proxy FuzzHTTPCheckAuth fuzz_http_check_auth

# Stage a libFuzzer dictionary of config/hook tokens for each target that benefits
# from grammar-aware mutation. Native Go (testing.F) ignores dictionaries, so this
# only helps the libFuzzer binaries built above.
if [ -f "$SRC/kubeport/.clusterfuzzlite/kubeport.dict" ]; then
  for target in fuzz_unmarshal_yaml fuzz_unmarshal_toml fuzz_load_with_inheritance \
                fuzz_parse_event_type fuzz_build_from_config fuzz_parse_svc_flag; do
    cp "$SRC/kubeport/.clusterfuzzlite/kubeport.dict" "$OUT/$target.dict"
  done
fi
