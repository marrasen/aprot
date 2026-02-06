module github.com/marrasen/aprot/e2e

go 1.25

require (
	github.com/marrasen/aprot v0.0.0
	github.com/marrasen/aprot/example/vanilla v0.0.0
)

require (
	github.com/go-json-experiment/json v0.0.0-20251027170946-4849db3c2f7e // indirect
	github.com/gorilla/websocket v1.5.1 // indirect
	golang.org/x/net v0.17.0 // indirect
)

replace (
	github.com/marrasen/aprot => ..
	github.com/marrasen/aprot/example/vanilla => ../example/vanilla
)
