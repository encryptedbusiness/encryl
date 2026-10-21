module github.com/encryptedbusiness/encryl/examples/uringhttp_chi

go 1.26.0

require (
	github.com/go-chi/chi/v5 v5.5.0
	github.com/encryptedbusiness/encryl v0.0.0
)

require golang.org/x/sys v0.49.0 // indirect

replace github.com/encryptedbusiness/encryl => ../..
