package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"

	"github.com/markpash/tailscale-sidecar/tsnet"
	"tailscale.com/client/tailscale"
	"tailscale.com/ipn"
	// "tailscale.com/tsnet"
)

type Binding struct {
	From uint16 `json:"from"`
	To   string `json:"to"`
	Tls  bool   `json:"tls"`
}

func loadBindings() ([]Binding, error) {
	bindingsPath := os.Getenv("TS_SIDECAR_BINDINGS")
	if bindingsPath == "" {
		bindingsPath = "/etc/ts-sidecar/bindings.json"
	}

	bindingsFile, err := os.Open(bindingsPath)
	if err != nil {
		return nil, err
	}
	defer bindingsFile.Close()

	d := json.NewDecoder(bindingsFile)

	var bindings []Binding
	if err := d.Decode(&bindings); err != nil {
		return nil, err
	}

	if len(bindings) == 0 {
		return nil, errors.New("bindings empty")
	}

	return bindings, nil
}

func newTsNetServer() tsnet.Server {
	hostname := os.Getenv("TS_SIDECAR_NAME")
	if hostname == "" {
		panic("TS_SIDECAR_NAME env var not set")
	}

	stateDir := os.Getenv("TS_SIDECAR_STATEDIR")
	if stateDir == "" {
		stateDir = "./tsstate"
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		panic("failed to create default state directory")
	}

	// logf := logger.Logf
	// statePath := stateDir
	// store, err := ipn.NewFileStore(statePath)
	// if err != nil {
	// 	log.Panic(err) // Probably not, but testing atm
	// }
	// logid := "tslib-TODO"

	// lb, err := ipnlocal.NewLocalBackend(logf, logid, store, nil, eng)
	// if err != nil {
	// 	log.Fatalln("NewLocalBackend: %v", err)
	// }
	// lb.SetVarRoot(stateDir)

	// ctropts := controlclient.Options{
	// 	ServerURL: "https://headscale.hilli.dk",
	// 	AuthKey: os.Getenv("TS_AUTHKEY"),
	// }

	tsNet := tsnet.Server{
		Dir:      stateDir,
		Hostname: hostname,
	}
	opts := ipn.Options{
		Ser
	}
	tsNet.Lb.Start()
	perfs := tsNet.Lb.Prefs()
	log.Println("Hey")
	perfs.ControlURL = "https://headscale.hilli.dk"
	tsNet.Lb.SetPrefs(perfs)
	log.Printf("perfs: %+v", perfs)
	log.Printf("tsNet: %+v", tsNet)
	return tsNet
}

func proxyBind(s *tsnet.Server, b *Binding) {
	ln, err := s.Listen("tcp", fmt.Sprintf(":%d", b.From))
	// ln, err := net.Listen("tcp", fmt.Sprintf(":%d", b.From))
	if err != nil {
		log.Println(err)
		return
	}

	if b.Tls {
		ln = tls.NewListener(ln, &tls.Config{
			GetCertificate: tailscale.GetCertificate,
		})
	}

	status, err := tailscale.Status(context.Background())
	if err != nil {
		panic(err)
	}
	log.Printf("=== Peers: %+v\n", status.Peers())

	log.Printf("started proxy bind from %d to %v (tls: %t)", b.From, b.To, b.Tls)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println(err)
			continue
		}

		go func(left net.Conn) {
			defer left.Close()
			right, err := s.Dial(context.Background(), "tcp", b.To)
			// right, err := net.Dial("tcp", b.To)
			if err != nil {
				log.Println(err)
				return
			}
			defer right.Close()

			var wg sync.WaitGroup
			proxyConn := func(a, b net.Conn) {
				defer wg.Done()
				byteCount, err := io.Copy(a, b)
				log.Printf("=== Copied %d bytes from %s to %s", byteCount, a.LocalAddr().String(), b.LocalAddr().String())
				if err != nil {
					log.Println(err)
					return
				}
			}

			wg.Add(2)
			go proxyConn(right, left)
			go proxyConn(left, right)

			wg.Wait()
		}(conn)
	}
}

func main() {
	// Apparently this envvar needs to be set for this to work!
	err := os.Setenv("TAILSCALE_USE_WIP_CODE", "true")
	if err != nil {
		panic(err)
	}

	bindings, err := loadBindings()
	if err != nil {
		panic(err)
	}

	s := newTsNetServer()
	// Start the tailnet manually since we want to detect peers first.
	err = s.Start()
	if err != nil {
		panic(err)
	}
	status, err := tailscale.Status(context.Background())
	if err != nil {
		panic(err)
	}
	log.Printf("=== Peers: %+v\n", status.Peers())
	for i, p := range status.Peers() {
		fmt.Printf("=== Peer: %+v: %+v\n", i, p)
	}

	var wg sync.WaitGroup
	for _, binding := range bindings {
		wg.Add(1)
		go func(binding Binding) {
			defer wg.Done()
			proxyBind(&s, &binding)
		}(binding)
	}
	wg.Wait()
}
