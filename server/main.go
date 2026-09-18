package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"golang.org/x/term"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	config := flag.String("config", "server.json", "server state file")
	setPassword := flag.String("set-password", "", "change the password for a local account and exit")
	flag.Parse()

	store, err := OpenStore(*config)
	if err != nil {
		log.Fatal(err)
	}
	if *setPassword != "" {
		password, err := readPassword()
		if err != nil {
			log.Fatal(err)
		}
		if err := store.SetPassword(*setPassword, password); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("password updated for %s in %s\n", *setPassword, *config)
		return
	}
	log.Printf("enterprise VPN server listening on %s", *addr)
	if err := http.ListenAndServe(*addr, NewHandler(store)); err != nil {
		log.Fatal(err)
	}
}

func readPassword() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "new password: ")
		password, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		return string(password), err
	}
	password, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(password, "\r\n"), nil
}
