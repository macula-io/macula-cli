module github.com/macula-io/macula-cli

go 1.27.0

require github.com/macula-io/macula-go v0.4.0

// TEMPORARY, for local build/test only -- see PLAN_CLOSE_SERVICE_AUTH_GAPS.md
// Phase 0/1. Remove once macula-go tags a release carrying CallWithUCAN and
// bump the require line above to it instead.
replace github.com/macula-io/macula-go => ../macula-go

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/cpuid/v2 v2.0.9 // indirect
	github.com/quic-go/quic-go v0.61.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	lukechampine.com/blake3 v1.4.1 // indirect
)
