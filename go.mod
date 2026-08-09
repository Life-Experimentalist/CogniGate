// The conformance suite (GW-10) is its own module, rooted here so that
// `go test ./conformance/...` works from the repository root as the spec
// requires.
//
// The gateway keeps its own go.mod under gateway/, which excludes that subtree
// from this module automatically — so nothing about the gateway's build,
// vendoring or CI changes because this file exists. That separation is not
// incidental: GW-10 requires the suite to exercise a gateway only through its
// public planes, so it must not be able to import gateway internals even by
// accident. There is deliberately no `replace` directive and no dependency on
// github.com/cognigate/gateway.
//
// The suite uses the standard library only, which is why there is no go.sum.
module github.com/cognigate/cognigate

go 1.26
