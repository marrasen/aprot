package main

import (
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/marrasen/aprot"
	"github.com/marrasen/aprot/example/vanilla/api"
)

func main() {
	tokenStore := api.NewTokenStore()
	state := api.NewSharedState(tokenStore)
	authMiddleware := api.AuthMiddleware(tokenStore)
	registry := api.NewRegistry(state, authMiddleware)

	server := aprot.NewServer(registry, aprot.ServerOptions{
		HeartbeatInterval: 5000,
		HeartbeatTimeout:  2000,
	})

	state.Broadcaster = server
	state.UserPusher = server

	sseHandler := server.HTTPTransport()

	mux := http.NewServeMux()
	mux.Handle("/ws", server)
	mux.Handle("/sse", http.StripPrefix("/sse", sseHandler))
	mux.Handle("/sse/", http.StripPrefix("/sse", sseHandler))

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
