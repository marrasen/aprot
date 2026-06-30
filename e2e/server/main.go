package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/e2e/e2eapi"
	"github.com/marrasen/aprot/example/vanilla/api"
)

func main() {
	tokenStore := api.NewTokenStore()
	state := api.NewSharedState(tokenStore)
	authMiddleware := api.AuthMiddleware(tokenStore)
	registry := api.NewRegistry(state, authMiddleware)

	// Add REST + validation handlers for e2e coverage of those surfaces.
	e2eapi.Register(registry)

	server := aprot.NewServer(registry)

	state.Broadcaster = server
	state.UserPusher = server

	sseHandler := server.HTTPTransport()
	restAdapter := aprot.NewRESTAdapter(registry)

	// Rejection server — always rejects connections for e2e testing.
	rejectRegistry := aprot.NewRegistry()
	rejectServer := aprot.NewServer(rejectRegistry)
	rejectServer.OnConnect(func(ctx context.Context, conn *aprot.Conn) error {
		return aprot.ErrConnectionRejected("invalid session")
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.Handle("/ws-reject", rejectServer)
	mux.Handle("/sse", http.StripPrefix("/sse", sseHandler))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseHandler))
	mux.Handle("/api/", http.StripPrefix("/api", restAdapter))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}

	// Print address as the first line of stdout — test setup reads this.
	fmt.Println(listener.Addr().String())

	if err := http.Serve(listener, mux); err != nil {
		log.Fatal(err)
	}
}
