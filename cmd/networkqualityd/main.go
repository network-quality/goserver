// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"strconv"
	"sync"

	"github.com/likexian/selfca"
	nqserver "github.com/network-quality/goserver"

	// Do *not* remove this import. Per https://pkg.go.dev/net/http/pprof:
	// The package is typically only imported for the side effect of registering
	// its HTTP handlers. The handled paths all begin with /debug/pprof/.
	// See -debug for how we use it.
	_ "net/http/pprof"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

var (
	insecurePublicPort = flag.Int("insecure-public-port", 0, "The port to listen on for HTTP measurement accesses")
	publicPort         = flag.Int("public-port", defaultSecurePublicPort, "The port to listen on for HTTPS/H2C/HTTP3 measurement accesses")

	listenAddr = flag.String("listen-addr", "localhost", "address to bind to")

	announce    = flag.Bool("announce", false, "announce this server using DNS-SD")
	createCert  = flag.Bool("create-cert", false, "generate self-signed certs")
	debug       = flag.Bool("debug", false, "enable debug mode")
	enableCORS  = flag.Bool("enable-cors", false, "enable CORS headers")
	enableH2C   = flag.Bool("enable-h2c", false, "enable h2c (non-TLS http/2 prior knowledge) mode")
	enableHTTP2 = flag.Bool("enable-http2", true, "enable HTTP/2")
	enableHTTP3 = flag.Bool("enable-http3", false, "enable HTTP/3")
	showVersion = flag.Bool("version", false, "Show version")

	socketSendBuffer = flag.Uint("socket-send-buffer-size", 0, "The size of the socket send buffer via TCP_NOTSENT_LOWAT. Zero/unset means to leave unset")

	enableL4s          = flag.Bool("enable-l4s", false, fmt.Sprintf("Enable L4S using the default congestion control algorithm, %s.", defaultL4SCongestionControlAlgorithm))
	enableL4sAlgorithm = flag.String("enable-l4s-algorithm", "", "Enable L4S using the specified congestion control algorithm")

	tosString    = flag.String("tos", "0", "set TOS for listening socket")
	certFilename = flag.String("cert-file", "", "cert to use")
	keyFilename  = flag.String("key-file", "", "key to use")

	configName  = flag.String("config-name", "networkquality.example.com", "domain to generate config for")
	publicName  = flag.String("public-name", "", "host to generate config for (same as -config-name if not specified)")
	contextPath = flag.String("context-path", "", "context-path if behind a reverse-proxy")
)

const (
	defaultInsecurePublicPort            = 4080
	defaultSecurePublicPort              = 4043
	defaultL4SCongestionControlAlgorithm = "prague"
)

func main() {
	flag.Parse()

	if *showVersion {
		fmt.Fprintf(os.Stdout, "networkqualityd %s\n", nqserver.GitVersion)
		os.Exit(0)
	}

	tosTemp, err := strconv.ParseUint(*tosString, 10, 8)
	if err != nil {
		log.Fatal(err)
	}
	tos := uint8(tosTemp)

	operatingCtx, operatingCtxCancel := context.WithCancel(context.Background())
	defer operatingCtxCancel()

	certSpecified := false
	if len(*certFilename) > 0 && len(*keyFilename) > 0 {
		certSpecified = true
	}

	if *createCert {
		if certSpecified {
			log.Fatal("--cert-file and --key-file cannot be used with --create-cert")
		}

		certSpecified = true
		var caCertificate []*x509.Certificate

		dir, err := os.MkdirTemp("", "nqd")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)

		config := selfca.Certificate{
			IsCA:       true,
			CommonName: "network-quality goserver Root CA",
			NotBefore:  time.Now(),
			NotAfter:   time.Now().Add(time.Duration(365*24) * time.Hour),
		}

		certificate, caKey, err := selfca.GenerateCertificate(config)
		if err != nil {
			log.Fatal(err)
		}

		caBaseName := filepath.Join(dir, "ca")
		if err = selfca.WriteCertificate(caBaseName, certificate, caKey); err != nil {
			log.Fatal(err)
		}

		caCertificate, err = x509.ParseCertificates(certificate)
		if err != nil {
			log.Fatal(err)
		}

		certificate, key, err := selfca.GenerateCertificate(selfca.Certificate{
			IsCA:          false,
			CommonName:    *configName,
			KeySize:       2048,
			NotBefore:     config.NotBefore,
			NotAfter:      config.NotAfter,
			Hosts:         []string{*configName},
			CAKey:         caKey,
			CACertificate: caCertificate[0],
		})
		if err != nil {
			log.Fatal(err)
		}

		baseName := filepath.Join(dir, *configName)
		if err = selfca.WriteCertificate(baseName, certificate, key); err != nil {
			log.Fatal(err)
		}

		*certFilename = fmt.Sprintf("%s.crt", baseName)
		*keyFilename = fmt.Sprintf("%s.key", baseName)
	}

	var cfg *tls.Config
	if certSpecified {
		cfg = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}

		cfg.Certificates = make([]tls.Certificate, 1)
		cfg.Certificates[0], err = tls.LoadX509KeyPair(*certFilename, *keyFilename)
		if err != nil {
			log.Fatal(err)
		}

		if *enableHTTP2 {
			cfg.NextProtos = []string{"h2"}
		}
	}

	if len(*publicName) == 0 {
		*publicName = *configName
	}

	portScheme := make(map[int]string)
	if *enableH2C || !certSpecified {
		*insecurePublicPort = defaultInsecurePublicPort
		portScheme[*insecurePublicPort] = "http"
	} else {
		portScheme[*publicPort] = "https"
		if *insecurePublicPort > 0 {
			portScheme[*insecurePublicPort] = "http"
		}
	}

	if *debug {
		go func() {
			debugListenPort := 9090
			debugListenAddr := fmt.Sprintf("%s:%d", *listenAddr, debugListenPort)
			server := &http.Server{
				Addr:              debugListenAddr,
				ReadHeaderTimeout: 3 * time.Second,
			}

			err := server.ListenAndServe()
			if err != nil {
				log.Fatal(err)
			}
		}()
	}

	var announceShutdowners []func()
	var servers []*http.Server

	var wg sync.WaitGroup
	wg.Add(len(portScheme))

	ips := make([]net.IP, 0)
	if *announce {
		// The user may give us a hostname (rather than an address to listen on). In order to
		// handle this situation, we will use DNS to convert it to an IP. As a result, we may
		// get back more than one address -- handle that!
		if addresses, lookupErr := net.LookupHost(*listenAddr); lookupErr == nil {
			for _, addr := range addresses {
				if parsedAddr := net.ParseIP(addr); parsedAddr != nil {
					ips = append(ips, parsedAddr)
				}
			}
		}
	}

	var mut sync.Mutex
	for port, scheme := range portScheme {
		var hostPort string
		if port == 80 || port == 443 {
			hostPort = *publicName
		} else {
			hostPort = fmt.Sprintf("%s:%d", *publicName, port)
		}

		m := &nqserver.Server{
			PublicHostPort: hostPort,
			PublicPort:     port,
			EnableCORS:     *enableCORS,
			ContextPath:    *contextPath,
			Scheme:         scheme,
		}

		if *debug {
			go m.PrintStats()
		}

		if scheme == "https" && *enableHTTP3 {
			m.EnableH3AltSvc = true
		}

		mux := http.NewServeMux()
		mux.HandleFunc(m.ContextPath+"/", m.ConfigHandler)       // NOTE: This will go away
		mux.HandleFunc(m.ContextPath+"/config", m.ConfigHandler) // NOTE: This will go away
		mux.HandleFunc(m.ContextPath+"/.well-known/nq", m.ConfigHandler)
		for pattern, handler := range nqserver.CountingBulkHandlers(m.ContextPath, *enableCORS, &m.BytesServed, &m.BytesReceived) {
			mux.HandleFunc(pattern, handler)
		}

		var nl net.Listener
		var err error

		listenConfig := net.ListenConfig{
			Control: func(network, address string, conn syscall.RawConn) error {
				if *socketSendBuffer > 0 {
					log.Printf("setting TCP_NOTSENT_LOWAT to %d", *socketSendBuffer)
					if err := setTCPNotSentLowat(conn, int(*socketSendBuffer)); err != nil {
						return err
					}
				}

				if *enableL4s || *enableL4sAlgorithm != "" {
					actualL4SCongestionControlAlgorithm := defaultL4SCongestionControlAlgorithm
					if *enableL4sAlgorithm != "" {
						actualL4SCongestionControlAlgorithm = *enableL4sAlgorithm
					}
					log.Printf("setting TCP_CONGESTION to %v", actualL4SCongestionControlAlgorithm)
					if err := setTCPL4S(conn, actualL4SCongestionControlAlgorithm); err != nil {
						return err
					}
				}

				if tos > 0 {
					log.Printf("Setting IP_TOS to %d", tos)
					if err := setIPTos(network, conn, int(tos)); err != nil {
						return err
					}
				}
				return nil
			},
		}

		nl, err = listenConfig.Listen(operatingCtx, "tcp", fmt.Sprintf("%s:%d", *listenAddr, port))
		if err != nil {
			log.Fatal(err)
		}

		if scheme == "https" {
			nl = tls.NewListener(nl, cfg)
		}

		mynl := nl

		log.Printf("Network Quality URL: %s://%s:%d%s/.well-known/nq", scheme, *configName, port, *contextPath)

		go func(scheme string, nl net.Listener, port int) {
			if *enableH2C {
				server := &http.Server{
					Handler:           h2c.NewHandler(mux, &http2.Server{}),
					ReadHeaderTimeout: 3 * time.Second,
				}
				mut.Lock()
				servers = append(servers, server)
				mut.Unlock()
				if err := server.Serve(nl); err != nil {
					log.Fatal(err)
				}
				wg.Done()
			} else {
				if scheme == "https" {
					if *enableHTTP3 {
						log.Printf("Enabling H3 on %q", fmt.Sprintf("%s:%d", *listenAddr, port))
						server := http3.Server{
							Handler:    mux,
							Addr:       fmt.Sprintf("%s:%d", *listenAddr, port),
							QuicConfig: &quic.Config{},
						}
						// No Shutdown(...) available for http3.Server

						go func() {
							if err := server.ListenAndServeTLS(*certFilename, *keyFilename); !errors.Is(err, http.ErrServerClosed) {
								log.Fatal(err)
							}
							wg.Done()
						}()
					}

					server := &http.Server{
						Handler:           mux,
						ReadHeaderTimeout: 3 * time.Second,
					}

					if *enableHTTP2 {
						log.Printf("Enabling H2 on %q", fmt.Sprintf("%s:%d", *listenAddr, port))
						if err := http2.ConfigureServer(server, &http2.Server{}); err != nil {
							log.Fatal(err)
						}
					}
					mut.Lock()
					servers = append(servers, server)
					mut.Unlock()

					if err := server.Serve(nl); !errors.Is(err, http.ErrServerClosed) {
						log.Fatalf("FATAL: %q", err)
					}
				} else {
					server := &http.Server{
						Handler:           mux,
						ReadHeaderTimeout: 3 * time.Second,
					}
					mut.Lock()
					servers = append(servers, server)
					mut.Unlock()
					if err := server.Serve(nl); !errors.Is(err, http.ErrServerClosed) {
						log.Fatalf("FATAL: %q", err)
					}
				}
			}
			wg.Done()
		}(scheme, mynl, port)

		// Setup announcer for https configuration port
		if *announce && scheme == "https" {
			announceResponder, announceHandle, err := configureAnnouncer(ips, *configName, port)
			if err != nil {
				log.Fatalf("Could not announce the server instance: %v", err)
			}

			go func() {
				if err := announceResponder.Respond(operatingCtx); err != nil {
					log.Fatal(err)
				}
			}()

			announceShutdowners = append(announceShutdowners, func() { announceResponder.Remove(announceHandle) })
		}
	}

	// The user can stop the server with SIGINT
	signalChannel := make(chan os.Signal, 1)   // make the channel buffered, per documentation.
	signal.Notify(signalChannel, os.Interrupt) // only Interrupt is guaranteed to exist on all platforms.

	<-signalChannel

	for _, server := range servers {
		if err := server.Shutdown(operatingCtx); err != nil {
			log.Printf("error shutting down: %s", err)
		}
	}

	wg.Wait()

	if *announce {
		log.Printf("Shutting down dnssd announcer")
		shutdownDone := make(chan interface{})
		go func() {
			for _, shutdowner := range announceShutdowners {
				shutdowner()
			}
			shutdownDone <- nil
		}()

		// Either wait for Remove to complete or another SIGINT
		select {
		case <-signalChannel:
		case <-shutdownDone:
		}
	}
}
