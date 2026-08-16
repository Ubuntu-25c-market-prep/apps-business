// Deliberately dependency-free.
//
// A scaffold that pulls in a module graph on day one makes every later
// question - why is the image 400MB, why did the build break, which CVE is
// ours - harder to answer than it needs to be. The standard library covers
// everything this service does, including the Prometheus text exposition
// format, which is a documented plain-text format rather than a protocol.
//
// There is no go.sum because there are no external modules. When the first
// real dependency arrives, `go mod tidy` creates one and it must be committed.
module github.com/Ubuntu-25c-market-prep/apps-business/storefront

go 1.23
