//go:build ignore
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"

	"golang.org/x/crypto/acme"
)

func main() {
	// Load CA cert
	caCert, err := os.ReadFile("/integration/tls/ca.pem")
	if err != nil {
		fmt.Fprintf(os.Stderr, "read CA: %v\n", err)
		os.Exit(1)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caCert)
	
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
		RootCAs: pool,
	}

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	client := &acme.Client{
		DirectoryURL: "https://acmepebble.example:14000/dir",
		Key:          key,
	}
	
	ctx := context.Background()
	
	// Discover
	dir, err := client.Discover(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "discover: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Directory OrderURL: %s\n", dir.OrderURL)

	// Register account
	acct := &acme.Account{}
	_, err = client.Register(ctx, acct, func(tosURL string) bool { return true })
	if err != nil {
		fmt.Fprintf(os.Stderr, "register: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Account registered")

	// Create order
	o, err := client.AuthorizeOrder(ctx, acme.DomainIDs("testdebug.mox1.example"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorize order: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Order URI: %q Status: %s FinalizeURL: %q\n", o.URI, o.Status, o.FinalizeURL)
	fmt.Printf("AuthzURLs: %v\n", o.AuthzURLs)

	if o.Status == acme.StatusReady {
		fmt.Println("Order already ready (authz reuse)")
	} else {
		// Process authz
		for _, aURL := range o.AuthzURLs {
			z, err := client.GetAuthorization(ctx, aURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "get authz: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Authz status: %s\n", z.Status)
			if z.Status != acme.StatusPending {
				continue
			}
			// Accept tls-alpn-01 challenge
			for _, ch := range z.Challenges {
				if ch.Type == "tls-alpn-01" {
					_, err = client.Accept(ctx, ch)
					if err != nil {
						fmt.Fprintf(os.Stderr, "accept: %v\n", err)
						os.Exit(1)
					}
					break
				}
			}
			// Wait for authz
			_, err = client.WaitAuthorization(ctx, aURL)
			if err != nil {
				fmt.Fprintf(os.Stderr, "wait authz: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Authz valid")
		}
		// Wait for order to become ready
		o, err = client.WaitOrder(ctx, o.URI)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wait order: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("After WaitOrder: URI: %q Status: %s FinalizeURL: %q CertURL: %q\n", o.URI, o.Status, o.FinalizeURL, o.CertURL)
	
	// Create CSR with a SEPARATE key (pebble rejects CSR using account key)
	certKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: []string{"testdebug.mox1.example"}}, certKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create CSR: %v\n", err)
		os.Exit(1)
	}

	// Finalize
	chain, certURL, err := client.CreateOrderCert(ctx, o.FinalizeURL, csr, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create order cert: %v\n", err)
		// Try to manually do the finalize to see what happens
		fmt.Fprintf(os.Stderr, "Trying manual WaitOrder with URI=%q\n", o.URI)
		
		// Also try to fetch the order manually using postAsGet
		res, err2 := http.Get(o.URI)
		if err2 != nil {
			fmt.Fprintf(os.Stderr, "manual GET order: %v\n", err2)
		} else {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			fmt.Fprintf(os.Stderr, "Manual GET order status=%d body=%s\n", res.StatusCode, body)
		}
		os.Exit(1)
	}
	fmt.Printf("Cert chain length: %d, certURL: %s\n", len(chain), certURL)
}
