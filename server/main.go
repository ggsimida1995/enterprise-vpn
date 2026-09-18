package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	config := flag.String("config", "server.json", "server state file")
	flag.Parse()

	store, err := OpenStore(*config)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("enterprise VPN server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, NewHandler(store)); err != nil {
		log.Fatal(err)
	}
}
